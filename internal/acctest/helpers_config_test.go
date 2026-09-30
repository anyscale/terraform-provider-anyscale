package acctest

import (
	"fmt"
	"strings"
)

// k8sConfigBlock renders the kubernetes_config nested block shared by the
// cloud/cloud_resource HCL test fixtures that use the "operator
// identity + zones" shape (distinct from the single-occurrence bare
// context/kubeconfig_path shape used elsewhere, which is not duplicated
// anywhere and so is left inline). identity is the caller's own
// already-formatted string - existing call sites mix an AWS IAM role ARN
// and a GCP service-account email in this same field, so the caller builds
// whichever one it needs and passes the final string through unchanged.
// redisEndpoint is omitted from the block entirely when empty, so existing
// callers that pass "" render byte-identical HCL to before this field existed.
func k8sConfigBlock(identity string, zones []string, redisEndpoint string) string {
	quoted := make([]string, len(zones))
	for i, z := range zones {
		quoted[i] = fmt.Sprintf("%q", z)
	}
	redisLine := ""
	if redisEndpoint != "" {
		redisLine = fmt.Sprintf("\n    redis_endpoint                  = %q", redisEndpoint)
	}
	return fmt.Sprintf(`  kubernetes_config {
    anyscale_operator_iam_identity  = "%s"
    zones                           = [%s]%s
  }`, identity, strings.Join(quoted, ", "), redisLine)
}
