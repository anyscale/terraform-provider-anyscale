package acctest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The static fixture cloud hibernates when idle. The first request that
// reaches its admin zone gets HTTP 599 ("Cloud <id> has been hibernated due
// to inactivity ...") and, as a side effect, starts the wake; the same call
// succeeds once the shard is up. Compute-config create is such a request
// (validate_config -> admin-zone ValidateConfig; VM stacks only, K8S returns
// before it), and no read endpoint reaches the admin zone, so the gate probes
// with a create. Without it the first test to touch an idle cloud fails.
//
// Waking is a harness concern: retrying a 599 inside the provider is unsafe
// until each path's write-before-raise ordering is verified.

const (
	wakeBound          = 12 * time.Minute // under the 60m data-source job timeout
	wakeInitialBackoff = 5 * time.Second
	wakeMaxBackoff     = 60 * time.Second

	hibernationStatus = 599
)

// wakeProbe issues one request that reaches the cloud's admin zone and
// returns its status and body. err is a transport-level failure.
type wakeProbe func(ctx context.Context) (status int, body string, err error)

type wakeOpts struct {
	bound          time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
	sleep          func(time.Duration)
	now            func() time.Time
}

func defaultWakeOpts() wakeOpts {
	return wakeOpts{
		bound:          wakeBound,
		initialBackoff: wakeInitialBackoff,
		maxBackoff:     wakeMaxBackoff,
		sleep:          time.Sleep,
		now:            time.Now,
	}
}

// errWakeInconclusive means the probe could not produce a status twice in a
// row (transport failure). It is not a verdict on the cloud.
var errWakeInconclusive = errors.New("cloud wake probe inconclusive")

// parseErrorDetail returns error.detail from a JSON error body, and whether
// the body parsed.
func parseErrorDetail(body string) (string, bool) {
	var parsed struct {
		Error struct {
			Detail string `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", false
	}
	return parsed.Error.Detail, true
}

// isHibernationResponse reports whether a 599 body is the hibernation error.
// Any other 599 body is not treated as asleep.
func isHibernationResponse(status int, body string) bool {
	if status != hibernationStatus {
		return false
	}
	detail, ok := parseErrorDetail(body)
	return ok && strings.Contains(strings.ToLower(detail), "hibernated")
}

// hibernationDetail returns error.detail from a hibernation body, or the raw
// body if it does not parse.
func hibernationDetail(body string) string {
	if detail, ok := parseErrorDetail(body); ok && detail != "" {
		return detail
	}
	return body
}

// awaitCloudAwake probes until the cloud answers with anything other than the
// hibernation 599, backing off between probes, for at most opts.bound. Every
// other status - including a real 4xx/5xx - is returned as "awake" and left
// for the calling test to hit and report; the gate never converts a failure
// into a pass, it only stops waiting.
func awaitCloudAwake(ctx context.Context, cloudLabel string, probe wakeProbe, opts wakeOpts, logf func(string, ...any)) error {
	start := opts.now()
	backoff := opts.initialBackoff
	probes := 0
	transportRetried := false
	var lastDetail string

	for {
		status, body, err := probe(ctx)
		probes++
		if err != nil {
			if transportRetried {
				return fmt.Errorf("%w: %v", errWakeInconclusive, err)
			}
			transportRetried = true
			opts.sleep(opts.initialBackoff)
			continue
		}
		if !isHibernationResponse(status, body) {
			if probes > 1 {
				logf("Cloud %s is awake after %s (%d probes)", cloudLabel, opts.now().Sub(start).Round(time.Second), probes)
			}
			return nil
		}

		lastDetail = hibernationDetail(body)
		elapsed := opts.now().Sub(start)
		if elapsed >= opts.bound {
			return fmt.Errorf("cloud %s still hibernated after %s (%d probes); last detail: %s. "+
				"This is an environment wait, not a code failure: the cloud is likely still starting, so re-run once it is awake",
				cloudLabel, elapsed.Round(time.Second), probes, lastDetail)
		}
		if probes == 1 {
			logf("Cloud %s is hibernated; waiting for it to wake (up to %s)", cloudLabel, opts.bound)
		}
		opts.sleep(backoff)
		if backoff *= 2; backoff > opts.maxBackoff {
			backoff = opts.maxBackoff
		}
	}
}

// wakeGate holds the once-per-binary outcome for one cloud. Concurrent callers
// serialize on mu, so a single probe sequence runs and the rest reuse its result.
type wakeGate struct {
	mu   sync.Mutex
	done bool
	err  error
}

var (
	wakeGates      = map[string]*wakeGate{}
	wakeGatesMutex sync.Mutex
)

// ensureCloudAwakeWith runs the gate for cloudID at most once per binary. A
// conclusive result (awake, or hibernation timeout) is cached, so parallel
// tests fail fast rather than each waiting out the bound. An inconclusive one
// is not cached: it returns nil so a transient network error never blocks the
// suite, and the next caller tries again.
func ensureCloudAwakeWith(ctx context.Context, cloudID, cloudLabel string, probe wakeProbe, opts wakeOpts, logf func(string, ...any)) error {
	wakeGatesMutex.Lock()
	g, ok := wakeGates[cloudID]
	if !ok {
		g = &wakeGate{}
		wakeGates[cloudID] = g
	}
	wakeGatesMutex.Unlock()

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		return g.err
	}
	err := awaitCloudAwake(ctx, cloudLabel, probe, opts, logf)
	if errors.Is(err, errWakeInconclusive) {
		logf("Warning: %v; continuing without confirming the cloud is awake", err)
		return nil
	}
	g.done, g.err = true, err
	return err
}

// ensureCloudAwake is the gate every helper that hands a test a cloud ID goes
// through. It fails the calling test if the cloud does not wake in time.
func ensureCloudAwake(t *testing.T, cloudID, cloudLabel string) {
	t.Helper()
	if cloudID == "" {
		return
	}
	probe := func(ctx context.Context) (int, string, error) {
		return probeCloudWithComputeConfigCreate(ctx, t, cloudID)
	}
	if err := ensureCloudAwakeWith(context.Background(), cloudID, cloudLabel, probe, defaultWakeOpts(), t.Logf); err != nil {
		t.Fatalf("test cloud is not available: %v", err)
	}
}

// cloudLabelFor names the cloud for messages: the known name when there is
// one, never a committed ID.
func cloudLabelFor(name string) string {
	if name == "" {
		return "(test cloud)"
	}
	return fmt.Sprintf("%q", name)
}

// probeCloudWithComputeConfigCreate creates a throwaway compute config, which
// reaches the admin zone with wake enabled, and archives it on success. It
// changes nothing on the cloud itself.
func probeCloudWithComputeConfigCreate(ctx context.Context, t *testing.T, cloudID string) (int, string, error) {
	client, err := GetTestClient()
	if err != nil {
		return 0, "", err
	}
	name := UniqueName(t, "wake")
	payload, err := json.Marshal(computeConfigVersionBody(cloudID, name, "m5.large"))
	if err != nil {
		return 0, "", err
	}
	resp, err := client.DoRequest(ctx, "POST", "/api/v2/compute_templates/", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}

	if resp.StatusCode == 200 || resp.StatusCode == 201 {
		var created struct {
			Result struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if json.Unmarshal(body, &created) == nil && created.Result.ID != "" {
			archive, aerr := client.DoRequest(ctx, "POST", fmt.Sprintf("/api/v2/compute_templates/%s/archive", created.Result.ID), nil)
			if aerr != nil {
				t.Logf("Warning: failed to archive wake-probe compute config %s: %v", created.Result.ID, aerr)
			} else {
				_ = archive.Body.Close()
			}
		}
	}
	return resp.StatusCode, string(body), nil
}
