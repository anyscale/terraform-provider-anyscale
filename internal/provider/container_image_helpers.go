package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// digestSettlePollTimeout and digestSettlePollInterval bound how long Create/Update wait,
// after a build already reports the terminal "succeeded" status, for its digest to become
// non-null. Mirrors the acctest bounded-poll idiom (destroyCheckPollTimeout /
// destroyCheckPollInterval in internal/acctest/helpers.go), applied here in production code
// because real applies can observe the same window, not just tests.
//
// The backend has two internal completion states that both surface as the single external
// status "succeeded": the image built and pushed with its cache upload still in flight
// (digest not yet populated), and the cache upload finished (digest populated). The
// transition between them is driven by an async event consumer with no documented bound, so
// a build can legitimately report succeeded with a nil digest for a few seconds.
// resource_container_image_build.go and resource_container_image_registry.go both expose a
// Computed digest sourced from a build, so they share this wait rather than each re-polling
// independently.
const (
	digestSettlePollTimeout  = 30 * time.Second
	digestSettlePollInterval = 2 * time.Second
)

// waitForBuildDigest polls a build already known to be in a terminal-succeeded state until
// its digest is populated, or digestSettlePollTimeout elapses. It returns the latest build
// observed and whether the digest settled.
//
// A timeout is an expected outcome (the cache upload can legitimately run long under backend
// load), not an error: callers should proceed with the last-seen build - digest possibly
// still null - and attach a warning diagnostic via AddDigestNotSettledWarning rather than
// fail the apply. The image itself is already built and usable regardless; digest is a
// nice-to-have pin that self-heals on a later refresh.
//
// If build.Digest is already non-null, this returns immediately without sleeping or making
// any request - the common case once the settle window has already passed (e.g. a Create
// that spent a while in waitForBuild for other reasons).
func waitForBuildDigest(ctx context.Context, client *Client, build *BuildResult) (latest *BuildResult, settled bool) {
	return waitForBuildDigestWithTiming(ctx, client, build, digestSettlePollTimeout, digestSettlePollInterval)
}

// waitForBuildDigestWithTiming is waitForBuildDigest with the timeout/interval as parameters
// so tests can prove the transition and timeout paths without paying digestSettlePollTimeout
// in wall-clock time. Production code should always call waitForBuildDigest instead - it pins
// the real timing constants.
func waitForBuildDigestWithTiming(ctx context.Context, client *Client, build *BuildResult, timeout, interval time.Duration) (latest *BuildResult, settled bool) {
	if build.Digest != nil {
		return build, true
	}

	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return build, false
		default:
		}

		if time.Now().After(deadline) {
			return build, false
		}
		time.Sleep(interval)

		refreshed, err := DoRequestAndParse[BuildResponse](
			ctx,
			client,
			"GET",
			fmt.Sprintf("/api/v2/builds/%s", build.ID),
			nil,
			http.StatusOK,
			http.StatusCreated,
		)
		if err != nil {
			tflog.Warn(ctx, "Failed to poll build while waiting for digest to settle", map[string]any{
				"build_id": build.ID,
				"error":    err.Error(),
			})
			continue
		}

		build = &refreshed.Result
		if build.Digest != nil {
			return build, true
		}
	}
}

// AddDigestNotSettledWarning attaches a warning diagnostic for the waitForBuildDigest
// timeout path. Never an error (see waitForBuildDigest) - worded identically wherever used
// since every caller shares the same underlying cause and resolution.
func AddDigestNotSettledWarning(diags *diag.Diagnostics, buildID string) {
	diags.AddWarning(
		"Container Image Digest Not Yet Available",
		fmt.Sprintf(
			"The build (%s) completed successfully, but its content digest was not yet available from the backend after waiting %s. "+
				"This can happen while the backend finishes uploading the image cache. The container image is fully built and usable; "+
				"digest will populate automatically on a future terraform plan or apply.",
			buildID, digestSettlePollTimeout,
		),
	)
}
