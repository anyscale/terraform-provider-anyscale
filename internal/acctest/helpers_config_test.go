package acctest

import (
	"fmt"
	"strings"
)

// awsConfigBlock renders the aws_config nested block shared by the
// cloud/cloud_resource HCL test fixtures that use the "vpc-test123 / one
// security group / two fixed subnets" shape. fullPrefix is the complete
// name prefix as used in the existing HCL (e.g. "tfacc-aws-basic") - it is
// not assumed to start with any fixed literal, since existing call sites
// already disagree on that (see gcpConfigBlock). randSuffix fills both IAM
// role ARNs, matching how every existing caller used it.
func awsConfigBlock(fullPrefix, randSuffix string) string {
	return fmt.Sprintf(`  aws_config {
    vpc_id             = "vpc-test123"
    subnet_ids         = ["subnet-test1", "subnet-test2"]
    security_group_ids = ["sg-test1"]

    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/%s-crossaccount-%s"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/%s-cluster-node-%s"
    external_id               = "anyscale-external-id-test"

    subnet_ids_to_az = {
      "subnet-test1" = "us-east-2a"
      "subnet-test2" = "us-east-2b"
    }
  }`, fullPrefix, randSuffix, fullPrefix, randSuffix)
}

// gcpConfigBlock renders the gcp_config nested block shared by the
// cloud/cloud_resource HCL test fixtures that use the "my-gcp-project /
// anyscale-vpc" shape. fullPrefix is the complete name prefix as used in
// the existing HCL - existing call sites use different conventions
// ("tfacc-gcp-basic" vs "tf-cres-gcp"), so this takes the whole prefix
// rather than assuming a shared literal. randSuffix fills the workload
// identity pool/provider and both service account emails, matching how
// every existing caller used it.
func gcpConfigBlock(fullPrefix, randSuffix string) string {
	return fmt.Sprintf(`  gcp_config {
    project_id                        = "my-gcp-project"
    vpc_name                          = "anyscale-vpc"
    subnet_names                      = ["anyscale-subnet-1", "anyscale-subnet-2"]
    firewall_policy_names             = ["anyscale-fw-ssh"]
    provider_name                     = "projects/123456789012/locations/global/workloadIdentityPools/%s-pool-%s/providers/%s-prov-%s"
    controlplane_service_account_email = "%s-cp-%s@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "%s-dp-%s@my-gcp-project.iam.gserviceaccount.com"
  }`, fullPrefix, randSuffix, fullPrefix, randSuffix, fullPrefix, randSuffix, fullPrefix, randSuffix)
}

// k8sConfigBlock renders the kubernetes_config nested block shared by the
// cloud/cloud_resource HCL test fixtures that use the "namespace + operator
// identity + zones" shape (distinct from the single-occurrence bare
// context/kubeconfig_path shape used elsewhere, which is not duplicated
// anywhere and so is left inline). identity is the caller's own
// already-formatted string - existing call sites mix an AWS IAM role ARN
// and a GCP service-account email in this same field, so the caller builds
// whichever one it needs and passes the final string through unchanged.
// redisEndpoint is omitted from the block entirely when empty, so existing
// callers that pass "" render byte-identical HCL to before this field existed.
func k8sConfigBlock(namespace, identity string, zones []string, redisEndpoint string) string {
	quoted := make([]string, len(zones))
	for i, z := range zones {
		quoted[i] = fmt.Sprintf("%q", z)
	}
	redisLine := ""
	if redisEndpoint != "" {
		redisLine = fmt.Sprintf("\n    redis_endpoint                  = %q", redisEndpoint)
	}
	return fmt.Sprintf(`  kubernetes_config {
    namespace                       = "%s"
    anyscale_operator_iam_identity  = "%s"
    zones                           = [%s]%s
  }`, namespace, identity, strings.Join(quoted, ", "), redisLine)
}
