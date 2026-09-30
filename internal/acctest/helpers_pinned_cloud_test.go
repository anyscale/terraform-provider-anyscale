package acctest

import "testing"

// pinnedVMCloud returns the pinned test cloud (GetTestCloudID: ANYSCALE_TEST_CLOUD_ID,
// ANYSCALE_TEST_CLOUD_NAME, then the static fixture by name) as a CloudInfo, for tests
// that deploy real workloads onto it. It deliberately does not take the first entry of
// GetAllVMClouds: that order is the org's cloud list order, so any other VM cloud in the
// shared org - another run's tfacc cloud, a hand-made fixture - could win and receive the
// deployment. The test fails if the pinned cloud is not a configured VM cloud.
func pinnedVMCloud(t *testing.T) CloudInfo {
	t.Helper()
	id := GetTestCloudID(t)
	cloud, ok := selectCloudByID(GetAllConfiguredClouds(t), id)
	if !ok {
		t.Fatalf("pinned test cloud %s is not among the configured clouds with a cloud resource", id)
	}
	if !cloud.IsVM() {
		t.Fatalf("pinned test cloud %s (%s) is %s, not a VM cloud", cloud.Name, id, cloud.ComputeStack)
	}
	return cloud
}

// selectCloudByID returns the cloud with the given ID, independent of list order.
func selectCloudByID(clouds []CloudInfo, id string) (CloudInfo, bool) {
	for _, c := range clouds {
		if c.ID == id {
			return c, true
		}
	}
	return CloudInfo{}, false
}

// A decoy VM cloud listed ahead of the pinned one must not be selected. The old selection
// (first entry of the provider-deduplicated VM list) picks the decoy; this is the case that
// sent real service deployments to a throwaway cloud.
func TestSelectCloudByIDIgnoresListOrder(t *testing.T) {
	decoy := CloudInfo{ID: "cld_decoy", Name: "tfacc-decoy", Provider: "AWS", ComputeStack: "VM"}
	pinned := CloudInfo{ID: "cld_pinned", Name: defaultKnownGoodCloudName, Provider: "AWS", ComputeStack: "VM"}
	clouds := []CloudInfo{decoy, pinned}

	if got, ok := selectCloudByID(clouds, pinned.ID); !ok || got.ID != pinned.ID {
		t.Fatalf("selectCloudByID picked %q (found=%v), want %q", got.ID, ok, pinned.ID)
	}
	if clouds[0].ID != decoy.ID {
		t.Fatal("positive control broken: the decoy must be listed first")
	}
	if _, ok := selectCloudByID(clouds, "cld_absent"); ok {
		t.Fatal("an absent ID must not resolve")
	}
}
