package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"k8s.io/apimachinery/pkg/labels"
)

// Wire model for GET/PUT /api/v2/clouds/{cloud_id}/deployment/{cloud_resource_id}/config.
// See docs/decisions/cloud-iam-mapping/README.md for the full contract this
// mirrors - in particular why cloud_provider/compute_stack are required in
// the request body and then discarded, and why an empty spec is a no-op
// rather than a wipe.

// CloudDeploymentConfigResponse is the {"result": {"spec": {...}}} envelope
// both GET and PUT return.
type CloudDeploymentConfigResponse struct {
	Result CloudDeploymentConfigResult `json:"result"`
}

// CloudDeploymentConfigResult wraps the spec object exactly as the API
// nests it - also reused as the PUT request body shape.
type CloudDeploymentConfigResult struct {
	Spec CloudDeploymentConfigSpec `json:"spec"`
}

// CloudDeploymentConfigSpec is the external CloudConfig shape
// (backend/go/appsidecar/cloud_config_model.go). cloud_provider and
// compute_stack are required on every PUT and validated server-side, then
// thrown away - never persisted, never exposed as schema attributes here.
// dataplane_iam_mapping deliberately has no omitempty: the server never
// omits the key either, always emitting {} when no mapping is set.
type CloudDeploymentConfigSpec struct {
	CloudDeploymentID       string                      `json:"cloud_deployment_id,omitempty"`
	CloudProvider           string                      `json:"cloud_provider,omitempty"`
	ComputeStack            string                      `json:"compute_stack,omitempty"`
	DataplaneIAMMapping     DataplaneIAMMappingWireSpec `json:"dataplane_iam_mapping"`
	UserTagAnnotationPrefix string                      `json:"user_tag_annotation_prefix,omitempty"`
}

// DataplaneIAMMappingWireSpec is the dataplane_iam_mapping sub-object. mode
// is server-derived and never read from a request (cloud_config_model.go:
// toInternalCloudConfig ignores it on decode, toExternalCloudConfig always
// stamps CUSTOMER_MANAGED on encode when rules is non-empty).
type DataplaneIAMMappingWireSpec struct {
	Mode         string                    `json:"mode,omitempty"`
	Rules        []DataplaneIAMMappingRule `json:"rules,omitempty"`
	FallbackRule string                    `json:"fallback_rule,omitempty"`
}

// DataplaneIAMMappingRule is one rule entry. Order within the containing
// slice is semantic (first match wins) - never sort or dedupe this.
type DataplaneIAMMappingRule struct {
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
}

const cloudIAMMappingConfigPathFormat = "/api/v2/clouds/%s/deployment/%s/config"

// getCloudDeploymentConfig performs the GET half of the config endpoint.
// Returns ErrNotFound (via DoRequestAndParse/DoRequestRaw) when the cloud or
// cloud resource does not exist, so callers can distinguish "gone" from a
// real transport failure.
func getCloudDeploymentConfig(ctx context.Context, client *Client, cloudID, cloudResourceID string) (*CloudDeploymentConfigSpec, error) {
	resp, err := DoRequestAndParse[CloudDeploymentConfigResponse](
		ctx, client, http.MethodGet,
		fmt.Sprintf(cloudIAMMappingConfigPathFormat, cloudID, cloudResourceID),
		nil, http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return &resp.Result.Spec, nil
}

// putCloudDeploymentConfig performs the PUT half. spec must carry
// cloud_provider/compute_stack (required-but-discarded) and, unless the
// caller genuinely intends to clear it, the current user_tag_annotation_prefix -
// this function does not fetch or preserve that field itself; see
// resource_cloud_iam_mapping.go's read-modify-write callers.
func putCloudDeploymentConfig(ctx context.Context, client *Client, cloudID, cloudResourceID string, spec CloudDeploymentConfigSpec) (*CloudDeploymentConfigSpec, error) {
	reqBody, err := MarshalRequestBody(CloudDeploymentConfigResult{Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cloud deployment config request: %w", err)
	}
	resp, err := doCloudIAMMappingConfigRequestRetry(ctx, client, http.MethodPut, cloudID, cloudResourceID, reqBody)
	if err != nil {
		return nil, err
	}
	return &resp.Result.Spec, nil
}

// cloudIAMMappingRetryInitialBackoff/MaxBackoff/MaxAttempts/BackoffFactor
// mirror resource_cloud_access.go's cloudAccessRetryX constants in shape,
// but the classifier below is deliberately wider - it also retries a bare
// 500, because this endpoint's PUT is a full, idempotent replace of exactly
// one field (see docs/decisions/cloud-iam-mapping/README.md's partial-failure
// section): a 500 can arrive AFTER the write already committed, when the
// downstream machine-pool propagation fails, so retrying never risks a
// double-apply of something non-idempotent.
var (
	cloudIAMMappingRetryInitialBackoff = 1 * time.Second
	cloudIAMMappingRetryMaxBackoff     = 8 * time.Second
)

const (
	cloudIAMMappingRetryMaxAttempts   = 3
	cloudIAMMappingRetryBackoffFactor = 2.0
)

func cloudIAMMappingRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *UnexpectedStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	return strings.Contains(err.Error(), "API request failed")
}

// doCloudIAMMappingConfigRequestRetry wraps the PUT with a bounded retry,
// following cloudAccessDoWriteRetry's shape exactly - body is rewound via
// io.Seeker before each retry after the first (MarshalRequestBody's
// underlying *bytes.Reader satisfies it).
func doCloudIAMMappingConfigRequestRetry(ctx context.Context, client *Client, method, cloudID, cloudResourceID string, body io.Reader) (*CloudDeploymentConfigResponse, error) {
	path := fmt.Sprintf(cloudIAMMappingConfigPathFormat, cloudID, cloudResourceID)
	backoff := cloudIAMMappingRetryInitialBackoff
	seeker, _ := body.(io.Seeker)

	var lastBody []byte
	var lastErr error
	for attempt := 1; attempt <= cloudIAMMappingRetryMaxAttempts; attempt++ {
		if attempt > 1 && seeker != nil {
			if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
				return nil, fmt.Errorf("could not rewind request body for retry attempt %d: %w", attempt, seekErr)
			}
		}

		lastBody, lastErr = DoRequestRaw(ctx, client, method, path, body, http.StatusOK)
		if lastErr == nil || !cloudIAMMappingRetryableError(lastErr) || attempt == cloudIAMMappingRetryMaxAttempts {
			break
		}

		tflog.Warn(ctx, "Transient failure writing cloud IAM mapping config, retrying", map[string]any{
			"method": method, "path": path, "attempt": attempt, "backoff": backoff.String(),
		})
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (after %d attempt(s), most recent failure: %s)", ctx.Err(), attempt, lastErr)
		case <-time.After(backoff):
		}
		backoff = time.Duration(float64(backoff) * cloudIAMMappingRetryBackoffFactor)
		if backoff > cloudIAMMappingRetryMaxBackoff {
			backoff = cloudIAMMappingRetryMaxBackoff
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	var result CloudDeploymentConfigResponse
	if err := json.Unmarshal(lastBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}
	return &result, nil
}

// resolvePrimaryCloudResourceID finds the cloud's primary (is_default)
// deployment, erroring explicitly on zero or multiple matches rather than
// picking one - mirrors the CLI's own resolution
// (cloud_controller.py:2920-2941) exactly, including "multiple" being a real
// error case rather than dead code: unlike findDefaultInCloudResources
// (which intentionally returns the first match for a different, tolerant
// caller), import must not guess.
func resolvePrimaryCloudResourceID(ctx context.Context, client *Client, cloudID string) (string, error) {
	results, err := listCloudResources(ctx, client, cloudID)
	if err != nil {
		return "", fmt.Errorf("failed to list cloud resources for cloud %q: %w", cloudID, err)
	}

	var primaryIDs []string
	for _, r := range results {
		if r.IsDefault {
			primaryIDs = append(primaryIDs, r.CloudResourceID)
		}
	}

	switch len(primaryIDs) {
	case 0:
		return "", fmt.Errorf("no primary cloud resource found for cloud %q", cloudID)
	case 1:
		return primaryIDs[0], nil
	default:
		return "", fmt.Errorf("multiple primary cloud resources found for cloud %q: %v - specify cloud_resource_id explicitly", cloudID, primaryIDs)
	}
}

// cloudIAMMappingRuleAttrTypes/Model back the rules ListNestedAttribute.
func cloudIAMMappingRuleAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"selector": types.StringType,
		"value":    types.StringType,
	}
}

func cloudIAMMappingRuleType() attr.Type {
	return types.ObjectType{AttrTypes: cloudIAMMappingRuleAttrTypes()}
}

// CloudIAMMappingRuleModel is one element of the rules list attribute.
type CloudIAMMappingRuleModel struct {
	Selector types.String `tfsdk:"selector"`
	Value    types.String `tfsdk:"value"`
}

// dataplaneIAMMappingRulesFromState converts the schema's rules
// types.List into the wire slice, preserving order - never sort, never
// dedupe. Returns nil (not an empty slice) for a null or empty list so the
// caller's DataplaneIAMMappingWireSpec.Rules stays omitempty-empty on the
// wire, matching "rules omitted" rather than "rules explicitly empty" (the
// backend treats both the same way today, but nil is the more honest
// representation of "no rules configured").
func dataplaneIAMMappingRulesFromState(ctx context.Context, rules types.List) ([]DataplaneIAMMappingRule, diag.Diagnostics) {
	var diags diag.Diagnostics
	if rules.IsNull() || rules.IsUnknown() || len(rules.Elements()) == 0 {
		return nil, diags
	}

	var models []CloudIAMMappingRuleModel
	diags.Append(rules.ElementsAs(ctx, &models, false)...)
	if diags.HasError() {
		return nil, diags
	}

	wireRules := make([]DataplaneIAMMappingRule, 0, len(models))
	for _, m := range models {
		wireRules = append(wireRules, DataplaneIAMMappingRule{
			Selector: m.Selector.ValueString(),
			Value:    m.Value.ValueString(),
		})
	}
	return wireRules, diags
}

// dataplaneIAMMappingRulesToState is getCloudDeploymentConfig's read-side
// mirror: builds the rules types.List Read should put in state. A nil/empty
// wire slice becomes types.ListNull, not an empty list - dataplane_iam_mapping
// has no omitempty and always round-trips as {} when unset, and {} must map
// to null per the repo's null-vs-empty contract rather than surface as a
// phantom empty-rules diff.
func dataplaneIAMMappingRulesToState(wireRules []DataplaneIAMMappingRule) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	elemType := cloudIAMMappingRuleType()
	if len(wireRules) == 0 {
		return types.ListNull(elemType), diags
	}

	models := make([]CloudIAMMappingRuleModel, 0, len(wireRules))
	for _, r := range wireRules {
		models = append(models, CloudIAMMappingRuleModel{
			Selector: types.StringValue(r.Selector),
			Value:    types.StringValue(r.Value),
		})
	}

	list, d := types.ListValueFrom(context.Background(), elemType, models)
	diags.Append(d...)
	return list, diags
}

// validIAMMappingSelectorKeys and validIAMMappingWorkloadTypeValues mirror
// cloud_config_model.go's toInternalCloudConfig switch exactly, so a plan-time
// rejection here matches what the server would 400 on - see
// docs/decisions/cloud-iam-mapping/README.md's validation table.
var (
	validIAMMappingSelectorKeys       = map[string]bool{"workload-type": true, "project": true, "user": true}
	validIAMMappingWorkloadTypeValues = map[string]bool{"job": true, "service": true, "workspace": true}
)

// iamMappingSelectorEmailRegex matches go/infra/config/cloud/util.go's own
// emailRegex byte-for-byte. A raw email address (the "user" selector's usual
// value) is NOT valid Kubernetes label-selector syntax on its own - the
// backend substitutes each match with its FNV-64 hash before parsing, and
// this validator must reproduce that exact substitution or it rejects every
// ordinary `user=someone@example.com` selector as invalid syntax, when the
// server would accept it.
var iamMappingSelectorEmailRegex = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`)

// hashIAMMappingSelectorEmails mirrors
// ParseDataplaneIAMMappingRuleSelector's regex substitution exactly (same
// pattern, same fnv.New64, same decimal formatting) so this validator
// parses precisely what the server parses.
func hashIAMMappingSelectorEmails(selector string) string {
	return iamMappingSelectorEmailRegex.ReplaceAllStringFunc(selector, func(email string) string {
		h := fnv.New64()
		_, _ = h.Write([]byte(email))
		return fmt.Sprintf("%d", h.Sum64())
	})
}

// validateIAMMappingSelector parses selector as a Kubernetes label selector
// (the same grammar and library the backend uses -
// go/infra/config/cloud/util.go imports the same k8s.io/apimachinery/pkg/labels)
// and checks it is selectable and restricted to the keys/values this
// endpoint accepts. Returns a human-readable problem description, or "" if
// the selector is valid.
func validateIAMMappingSelector(selector string) string {
	parsed, err := labels.Parse(hashIAMMappingSelectorEmails(selector))
	if err != nil {
		return fmt.Sprintf("invalid selector syntax: %s", err)
	}

	requirements, selectable := parsed.Requirements()
	if !selectable {
		return "selector is not selectable (e.g. it is unsatisfiable)"
	}

	for _, req := range requirements {
		key := req.Key()
		if !validIAMMappingSelectorKeys[key] {
			return fmt.Sprintf("invalid selector key %q - must be one of workload-type, project, user", key)
		}
		if key == "workload-type" {
			for _, value := range req.Values().List() {
				if !validIAMMappingWorkloadTypeValues[value] {
					return fmt.Sprintf("invalid workload-type value %q - must be one of job, service, workspace", value)
				}
			}
		}
	}
	return ""
}
