package acctest

import "testing"

// The real-infra IAM mapping tests overwrite a cloud's whole mapping, so they
// must refuse the shared static fixture even when ANYSCALE_TEST_IAM_MAPPING_CLOUD_ID names
// it explicitly. This runs in the plain unit lane, where the guard itself can
// be proven without real infrastructure.
func TestRefuseSharedIAMMappingTestCloud(t *testing.T) {
	if err := refuseSharedIAMMappingTestCloud(defaultKnownGoodCloudName); err == nil {
		t.Fatalf("expected the shared fixture %q to be refused", defaultKnownGoodCloudName)
	}
	if err := refuseSharedIAMMappingTestCloud("tfacc-iam-mapping-dedicated"); err != nil {
		t.Fatalf("a dedicated cloud must be accepted, got: %v", err)
	}
}
