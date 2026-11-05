package provider

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// The by-ID role binding routes check the caller's permission on the binding
// object itself, and a newly granted binding's permission takes a few seconds
// to propagate (about 5s measured). Until then GET and DELETE answer 403 with
// the same detail a revoked or unknown ID gets, so the text alone cannot tell
// the race from a binding that is gone. Callers decide eligibility from
// something else: the principal listing (checked against manage-IAM on the
// scope, and current immediately) or the binding's age.
var (
	roleBindingByIDRetryInitialInterval = 500 * time.Millisecond
	roleBindingByIDRetryMaxInterval     = 4 * time.Second
	roleBindingByIDRetryMaxWait         = 30 * time.Second
)

// roleBindingByIDRecentWindow is how old a binding may be and still have its
// by-ID 403 treated as propagation lag rather than a real refusal.
const roleBindingByIDRecentWindow = 2 * time.Minute

// isRoleBindingByIDPropagation403 reports whether err is the by-ID permission
// refusal that a not-yet-propagated binding also gets. Any other 403, such as
// the refusal to revoke the caller's own binding, is never retried.
func isRoleBindingByIDPropagation403(err error) bool {
	if !isHTTPStatus(err, http.StatusForbidden) {
		return false
	}
	detail := extractAPIErrorDetail(err)
	return strings.HasPrefix(detail, "You may not read role binding") ||
		strings.HasPrefix(detail, "You may not revoke role binding")
}

// retryRoleBindingByID calls call until it succeeds, fails with something
// other than the propagation 403, keepRetrying (when set) says to stop, or
// roleBindingByIDRetryMaxWait has been spent waiting. It returns the last
// result. keepRetrying runs before each wait, after a propagation 403.
func retryRoleBindingByID[T any](ctx context.Context, roleBindingID string, call func() (T, error), keepRetrying func() bool) (T, error) {
	interval := roleBindingByIDRetryInitialInterval
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		result, err := call()
		if err == nil || !isRoleBindingByIDPropagation403(err) || waited >= roleBindingByIDRetryMaxWait {
			return result, err
		}
		if keepRetrying != nil && !keepRetrying() {
			return result, err
		}
		tflog.Debug(ctx, "Role binding not yet readable by ID; retrying", map[string]any{
			"role_binding_id": roleBindingID, "attempt": attempt, "next_wait": interval.String(),
		})
		select {
		case <-ctx.Done():
			return result, err
		case <-time.After(interval):
		}
		waited += interval
		interval *= 2
		if interval > roleBindingByIDRetryMaxInterval {
			interval = roleBindingByIDRetryMaxInterval
		}
	}
}

// getRoleBindingWithRetry is getRoleBinding, retried while the binding may be
// too new to read by ID.
func getRoleBindingWithRetry(ctx context.Context, client *Client, roleBindingID string) (*roleBindingResult, error) {
	return retryRoleBindingByID(ctx, roleBindingID, func() (*roleBindingResult, error) {
		return getRoleBinding(ctx, client, roleBindingID)
	}, nil)
}
