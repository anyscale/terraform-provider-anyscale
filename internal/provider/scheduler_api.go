package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Anyscale Scheduler API wire models and transport helpers.
//
// This is the transport layer only: Go structs mirroring the request and
// response bodies of /api/v2/scheduler/*, plus the error translation those
// endpoints need. It deliberately contains no Terraform types and no schema,
// so it is identical whichever way the config document is ultimately modelled
// in HCL.
//
// The config document tree below mirrors the backend's SchedulerConfig
// Pydantic model (backend/server/database/api/global_resource_scheduler.py)
// field for field. Every sub-model there declares `extra = "forbid"`, so a
// key this provider invents - a typo, a field carried over from the retired
// machine-pool surface - is rejected outright with a 422 rather than being
// silently ignored. Keep these structs in exact correspondence with that
// model; do not add convenience fields.
//
// Every optional scalar is a pointer and every optional list is a nil-able
// slice, both with `omitempty`.
//
// For scalars the pointer is load-bearing rather than cosmetic: the backend
// treats an unset quota as "unlimited" and an explicit 0 as "block this
// resource entirely", so serializing an absent value as 0 would invert the
// meaning of the field. The same holds for every optional nested struct, where
// `omitempty` tests only nilness - an unconditionally allocated empty struct
// serializes as `{}` and asserts the section, which is not the same as omitting
// it.
//
// For slices it is NOT the nilness that carries the meaning: `omitempty` omits
// any len-0 slice, so nil and an allocated empty slice are indistinguishable on
// the wire. Writing them nil-able is a readability convention, not a guard.
// Do not rely on it as one, and do not "harden" a slice field by allocating it.

// --- Enum values -------------------------------------------------------------
//
// Declared as constants rather than a Go enum type because they cross the wire
// as plain strings and are surfaced to practitioners verbatim; the value lists
// are what a schema validator (or a docs page) enumerates.

const (
	schedulerOperatorIn           = "in"
	schedulerOperatorNotIn        = "not_in"
	schedulerOperatorExists       = "exists"
	schedulerOperatorDoesNotExist = "does_not_exist"
)

const (
	schedulerOnViolationReject      = "reject"
	schedulerOnViolationForceUpdate = "force_update"
)

// The set of accepted covered_resources values (cpu, gpu, memory_gb, tpu at the
// time of writing) is deliberately NOT mirrored here. It is a closed enum
// server-side, but a growing one - tpu was added after cpu and gpu, and more
// accelerator names will follow. Hardcoding it would reject a value the backend
// accepts until the provider is re-released; the plan-time validate call
// returns the authoritative list in its error instead.

// schedulerOperators, schedulerOnViolationActions and the three preemption
// value lists exist so a validator and the docs can share one definition with
// the wire layer.
var (
	schedulerOperators        = []string{schedulerOperatorIn, schedulerOperatorNotIn, schedulerOperatorExists, schedulerOperatorDoesNotExist}
	schedulerOnViolationActs  = []string{schedulerOnViolationReject, schedulerOnViolationForceUpdate}
	schedulerReclaimPolicies  = []string{"never", "lower_priority", "any"}
	schedulerBorrowPolicies   = []string{"never", "lower_priority"}
	schedulerWithinQueuePolic = []string{"never", "lower_priority"}
)

// --- Config document ---------------------------------------------------------

// SchedulerMatchExpression is one label-matching clause. The backend enforces a
// cross-field rule these structs cannot: values must be non-empty for the "in"
// and "not_in" operators and must be absent for "exists"/"does_not_exist".
type SchedulerMatchExpression struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

// SchedulerResourceFlavor names a class of machine the scheduler can place work
// on. AdvancedInstanceConfig is a free-form provider-specific blob the backend
// stores opaquely, so it is modelled as an arbitrary JSON object here too.
type SchedulerResourceFlavor struct {
	Name                   string                     `json:"name"`
	Selector               []SchedulerMatchExpression `json:"selector,omitempty"`
	AdvancedInstanceConfig map[string]any             `json:"advanced_instance_config,omitempty"`
}

// SchedulerResourceQuotaSpec is a per-resource quota inside a flavor quota.
//
// All three limits are *float64 because unset means unlimited while 0 means
// blocked - see the pointer note in this file's header comment. The backend
// additionally requires every value to be >= 0, to carry at most three decimal
// places, and LendingLimit to be <= NominalQuota.
type SchedulerResourceQuotaSpec struct {
	Name           string   `json:"name"`
	NominalQuota   *float64 `json:"nominal_quota,omitempty"`
	LendingLimit   *float64 `json:"lending_limit,omitempty"`
	BorrowingLimit *float64 `json:"borrowing_limit,omitempty"`
}

// SchedulerFlavorQuota binds quotas to a named flavor. Resource names within
// one flavor quota must be unique.
type SchedulerFlavorQuota struct {
	Name      string                       `json:"name"`
	Resources []SchedulerResourceQuotaSpec `json:"resources,omitempty"`
}

// SchedulerResourceGroup groups quotas by the resource axes they cover.
// CoveredResources and Flavors are both required and must be non-empty;
// CoveredResources must be a duplicate-free subset of
// schedulerAllowedResourceNames.
type SchedulerResourceGroup struct {
	CoveredResources []string               `json:"covered_resources"`
	Flavors          []SchedulerFlavorQuota `json:"flavors"`
}

// SchedulerPreemptionPolicy controls when queued work may be preempted. Each
// field is a *string so "not configured" stays distinguishable from an
// explicitly chosen "never", which are different instructions to the backend.
type SchedulerPreemptionPolicy struct {
	ReclaimWithinCohort *string `json:"reclaim_within_cohort,omitempty"`
	BorrowWithinCohort  *string `json:"borrow_within_cohort,omitempty"`
	WithinResourceQueue *string `json:"within_resource_queue,omitempty"`
}

// SchedulerResourceQueue is a named queue. Queues sharing a CohortName may
// lend and borrow quota between each other.
type SchedulerResourceQueue struct {
	Name           string                     `json:"name"`
	Preemption     *SchedulerPreemptionPolicy `json:"preemption,omitempty"`
	ResourceGroups []SchedulerResourceGroup   `json:"resource_groups,omitempty"`
	CohortName     *string                    `json:"cohort_name,omitempty"`
}

// SchedulerPriorityPolicy bounds the priority a workload matched by a rule may
// request. The backend requires Min <= Max and Default within [Min, Max].
type SchedulerPriorityPolicy struct {
	Default     *int64  `json:"default,omitempty"`
	Min         *int64  `json:"min,omitempty"`
	Max         *int64  `json:"max,omitempty"`
	OnViolation *string `json:"on_violation,omitempty"`
}

// SchedulerSchedulingRule routes workloads matching Selector into ResourceQueue.
//
// ResourceQueue is a cross-reference into the document's own resource_queues
// list. The backend rejects a dangling reference with a 400 whose body is a
// plain sentence, NOT the structured 422 field errors - see
// translateSchedulerAPIError. No Terraform schema validator can catch that
// class of error, however precisely the tree is typed, because it depends on
// the rest of the document.
type SchedulerSchedulingRule struct {
	Selector       []SchedulerMatchExpression `json:"selector,omitempty"`
	ResourceQueue  string                     `json:"resource_queue"`
	PriorityPolicy *SchedulerPriorityPolicy   `json:"priority_policy,omitempty"`
}

// SchedulerRecyclePolicy governs node reuse. RotationInterval and
// MaxIdleDuration are duration strings the backend parses; MaxWorkloads must
// be >= 1 when set.
type SchedulerRecyclePolicy struct {
	RotationInterval *string `json:"rotation_interval,omitempty"`
	MaxWorkloads     *int64  `json:"max_workloads,omitempty"`
	MaxIdleDuration  *string `json:"max_idle_duration,omitempty"`
}

// SchedulerConfig is the whole config document. All four sections are optional;
// an empty document is a valid (if inert) config.
type SchedulerConfig struct {
	ResourceFlavors []SchedulerResourceFlavor `json:"resource_flavors,omitempty"`
	ResourceQueues  []SchedulerResourceQueue  `json:"resource_queues,omitempty"`
	SchedulingRules []SchedulerSchedulingRule `json:"scheduling_rules,omitempty"`
	RecyclePolicy   *SchedulerRecyclePolicy   `json:"recycle_policy,omitempty"`
}

// --- Request/response envelopes ----------------------------------------------

// ApplySchedulerConfigRequest is the body of POST /api/v2/scheduler/config and
// POST /api/v2/scheduler/config/validate.
type ApplySchedulerConfigRequest struct {
	Config SchedulerConfig `json:"config"`
}

// ApplySchedulerConfigResponse is the body of a successful apply. Version is
// monotonically increasing per organization; there is no route that deletes or
// rolls back a version.
type ApplySchedulerConfigResponse struct {
	Result struct {
		Version int64 `json:"version"`
	} `json:"result"`
}

// SchedulerConfigResponse is the body of GET /api/v2/scheduler/config.
//
// When no config has ever been applied this endpoint answers 404 with a real
// error body ("No active scheduler config found."), not an empty 200 - see
// getActiveSchedulerConfig.
type SchedulerConfigResponse struct {
	Result struct {
		Version   int64           `json:"version"`
		IsActive  bool            `json:"is_active"`
		CreatedAt string          `json:"created_at"`
		CreatorID string          `json:"creator_id"`
		Config    SchedulerConfig `json:"config"`
	} `json:"result"`
}

// SchedulerConfigVersionSummary is one entry of the version history.
type SchedulerConfigVersionSummary struct {
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
	CreatorID string `json:"creator_id"`
}

// ListSchedulerConfigVersionsResponse is the body of GET
// /api/v2/scheduler/config/versions.
//
// Note the envelope: this endpoint returns {"results": [...], "metadata": {...}},
// NOT the {"result": {...}} shape every other model in this provider uses.
// Confirmed against the live API, not inferred. Reusing a `result` envelope
// here unmarshals cleanly into an empty slice and looks like "no versions."
type ListSchedulerConfigVersionsResponse struct {
	Results  []SchedulerConfigVersionSummary `json:"results"`
	Metadata struct {
		Total           int64   `json:"total"`
		NextPagingToken *string `json:"next_paging_token"`
	} `json:"metadata"`
}

// --- Transport ---------------------------------------------------------------

const (
	schedulerConfigPath         = "/api/v2/scheduler/config"
	schedulerConfigValidatePath = "/api/v2/scheduler/config/validate"
	schedulerConfigVersionsPath = "/api/v2/scheduler/config/versions"
)

// ErrSchedulerConfigNotFound is returned by getActiveSchedulerConfig when the
// organization has no active scheduler config.
//
// This is a distinct sentinel rather than a reuse of ErrNotFound because the
// two mean different things to a caller here: ErrNotFound on this endpoint is
// the ONLY way "nothing applied yet" is reported, and Read must treat it as a
// normal empty state rather than an error. Passing http.StatusNotFound as an
// accepted status instead would be the known-bad path: DoRequestAndParse would
// return a non-nil pointer to a zero-valued SchedulerConfigResponse, which is
// indistinguishable from a genuinely empty config document.
var ErrSchedulerConfigNotFound = errors.New("no active scheduler config")

// ErrSchedulerNotEnabled is returned whenever the backend refuses a
// /api/v2/scheduler call because the organization lacks the scheduler
// admission flag. Callers translate it into a plain diagnostic instead of
// surfacing the backend's internal wording.
var ErrSchedulerNotEnabled = errors.New("anyscale scheduler is not enabled for this organization")

// schedulerNotEnabledDetail is the name-agnostic half of the backend's 403
// text from the scheduler admission gate. The gate's message currently leads
// with the surface's former internal abbreviation, which this rename is
// retiring - so matching on that abbreviation would break the moment upstream
// rewords its own sentence, which is a cosmetic change nothing would flag.
// This clause is house phrasing shared by unrelated capability gates upstream,
// making it the stable half.
//
// Deliberately biased toward over-matching. A false positive costs a warning
// that still quotes the server's own reason, so the practitioner sees the true
// cause regardless. A false negative sends both fail-open paths back to hard
// errors and blocks plan workspace-wide - the exact failure the fail-open
// behavior exists to prevent, reintroduced silently. The 403 status
// requirement below is what keeps an unrelated body from matching at all.
const schedulerNotEnabledDetail = "not enabled for this organization"

// isSchedulerNotEnabled reports whether err is a 403 raised by the scheduler
// admission gate.
func isSchedulerNotEnabled(err error) bool {
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusForbidden {
		return false
	}
	return strings.Contains(statusErr.Body, schedulerNotEnabledDetail)
}

// translateSchedulerAPIError converts a transport error from any
// /api/v2/scheduler call into an error whose text a practitioner can act on.
//
// Three cases the generic wrapper handles badly:
//
//   - 403 from the admission gate. The backend's text currently leads with the
//     surface's former abbreviation - as of writing, "GRS is not enabled for
//     this organization" - which appears nowhere in this provider or in
//     Anyscale's public docs. Replaced with wording that names the product and
//     says what to do. That quoted sentence is an illustration of today's
//     wording, not the match target: schedulerNotEnabledDetail deliberately
//     does NOT key on the abbreviation, and re-narrowing it to match this
//     example would reintroduce the defect its comment describes.
//   - 422 field validation. FastAPI returns a structured detail array whose
//     `loc` path pinpoints the offending field; flattened into one
//     "field: message" line per error so the practitioner does not have to
//     read raw JSON.
//   - everything else. Falls through to extractAPIErrorDetail, which surfaces
//     the backend's own sentence. The 400-class cross-reference errors (a rule
//     naming a queue that does not exist) land here deliberately: their text is
//     already a complete explanation and inventing our own would only lose
//     detail.
func translateSchedulerAPIError(err error) error {
	if err == nil {
		return nil
	}
	if isSchedulerNotEnabled(err) {
		return fmt.Errorf("%w: the Anyscale Scheduler is not enabled for this organization. "+
			"It is gated by an organization-level admission flag; contact Anyscale support to have it enabled",
			ErrSchedulerNotEnabled)
	}
	if detail := schedulerValidationDetail(err); detail != "" {
		return errors.New(detail)
	}
	return errors.New(extractAPIErrorDetail(err))
}

// schedulerValidationDetail flattens a FastAPI 422 body into readable lines,
// returning "" when err is not a 422 carrying that shape.
//
// The `loc` path's first element is always the request-body marker ("body"),
// which is noise to a practitioner reading a Terraform diagnostic, so it is
// dropped; the rest is joined with "." to give a path resembling the config
// document itself (e.g. "config.resource_queues.0.name").
func schedulerValidationDetail(err error) string {
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnprocessableEntity {
		return ""
	}

	var body struct {
		Detail []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		} `json:"detail"`
	}
	if jsonErr := json.Unmarshal([]byte(statusErr.Body), &body); jsonErr != nil || len(body.Detail) == 0 {
		return ""
	}

	lines := make([]string, 0, len(body.Detail))
	for _, d := range body.Detail {
		parts := make([]string, 0, len(d.Loc))
		for i, seg := range d.Loc {
			if i == 0 {
				if s, ok := seg.(string); ok && s == "body" {
					continue
				}
			}
			parts = append(parts, fmt.Sprintf("%v", seg))
		}
		if len(parts) == 0 {
			lines = append(lines, d.Msg)
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", strings.Join(parts, "."), d.Msg))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// getActiveSchedulerConfig reads the organization's active scheduler config.
//
// Returns ErrSchedulerConfigNotFound (wrapped) when none has been applied.
// http.StatusNotFound is deliberately NOT passed as an accepted status; see
// ErrSchedulerConfigNotFound's comment for why that path is unsafe here.
func getActiveSchedulerConfig(ctx context.Context, client *Client) (*SchedulerConfigResponse, error) {
	resp, err := DoRequestAndParse[SchedulerConfigResponse](ctx, client, http.MethodGet, schedulerConfigPath, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w", ErrSchedulerConfigNotFound)
		}
		return nil, translateSchedulerAPIError(err)
	}
	return resp, nil
}

// applySchedulerConfig POSTs a new version of the config document and returns
// the version number the backend assigned.
//
// Applying is monotonic and additive: it never replaces a version in place and
// there is no route that removes one.
func applySchedulerConfig(ctx context.Context, client *Client, config SchedulerConfig) (int64, error) {
	payload, err := json.Marshal(ApplySchedulerConfigRequest{Config: config})
	if err != nil {
		return 0, fmt.Errorf("failed to encode scheduler config: %w", err)
	}
	resp, err := DoRequestAndParse[ApplySchedulerConfigResponse](
		ctx, client, http.MethodPost, schedulerConfigPath, bytes.NewReader(payload), http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		return 0, translateSchedulerAPIError(err)
	}
	return resp.Result.Version, nil
}

// validateSchedulerConfig checks a config document without applying it.
//
// Non-mutating and confirmed to answer 204 for a valid document. This is the
// only mechanism that can catch the cross-reference errors described on
// SchedulerSchedulingRule, so it is worth calling even when the schema has
// already validated every individual field.
func validateSchedulerConfig(ctx context.Context, client *Client, config SchedulerConfig) error {
	payload, err := json.Marshal(ValidateSchedulerConfigRequest{Config: config})
	if err != nil {
		return fmt.Errorf("failed to encode scheduler config: %w", err)
	}
	if _, err := DoRequestRaw(
		ctx, client, http.MethodPost, schedulerConfigValidatePath, bytes.NewReader(payload),
		http.StatusOK, http.StatusNoContent,
	); err != nil {
		translated := translateSchedulerAPIError(err)
		if schedulerServerEvaluatedDocument(err) {
			return translated
		}
		// The server never got as far as reading the document - transport
		// failure, timeout, 401, 403, 5xx. Callers must be able to tell this
		// apart from a real rejection, because reporting "the API rejected
		// your config" for an expired token or a 503 is a false diagnostic,
		// and hard-failing on it makes `terraform plan` impossible for reasons
		// that have nothing to do with the config.
		return &schedulerValidationUnavailableError{detail: translated.Error()}
	}
	return nil
}

// ErrSchedulerValidationUnavailable marks a validate call that could not be
// performed, as opposed to one that ran and rejected the document.
var ErrSchedulerValidationUnavailable = errors.New("scheduler config validation could not be performed")

// schedulerValidationUnavailableError carries the underlying diagnostic
// verbatim - Error() is the translated detail, unprefixed - while remaining
// matchable with errors.Is. Wrapping with fmt.Errorf("%w: ...") would prepend
// a sentinel string to every message a caller renders.
type schedulerValidationUnavailableError struct{ detail string }

func (e *schedulerValidationUnavailableError) Error() string { return e.detail }
func (e *schedulerValidationUnavailableError) Unwrap() error {
	return ErrSchedulerValidationUnavailable
}

// schedulerServerEvaluatedDocument reports whether the backend actually read
// the submitted document and formed an opinion about it. 422 is FastAPI's
// structured field validation; 400 is the cross-reference check. Everything
// else - including a non-status transport error - means the answer is unknown,
// not "invalid".
func schedulerServerEvaluatedDocument(err error) bool {
	var statusErr *UnexpectedStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	return statusErr.StatusCode == http.StatusBadRequest ||
		statusErr.StatusCode == http.StatusUnprocessableEntity
}

// ValidateSchedulerConfigRequest is the body of the validate endpoint. It is
// shape-identical to ApplySchedulerConfigRequest but kept separate so the two
// can diverge without one silently changing the other.
type ValidateSchedulerConfigRequest struct {
	Config SchedulerConfig `json:"config"`
}

// listSchedulerConfigVersions returns the config version history, newest first
// as the backend orders it.
func listSchedulerConfigVersions(ctx context.Context, client *Client) ([]SchedulerConfigVersionSummary, error) {
	resp, err := DoRequestAndParse[ListSchedulerConfigVersionsResponse](
		ctx, client, http.MethodGet, schedulerConfigVersionsPath, nil, http.StatusOK,
	)
	if err != nil {
		return nil, translateSchedulerAPIError(err)
	}
	return resp.Results, nil
}
