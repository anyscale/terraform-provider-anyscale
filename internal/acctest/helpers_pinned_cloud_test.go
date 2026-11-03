package acctest

import (
	"fmt"
	"strings"
	"testing"
)

// testPlatform names the five platforms the real-infra tests target. One static fixture cloud
// per platform is selected with ANYSCALE_TEST_CLOUD_NAME (or ANYSCALE_TEST_CLOUD_ID).
type testPlatform string

const (
	platformAWSVM testPlatform = "AWS-VM"
	platformGCPVM testPlatform = "GCP-VM"
	platformEKS   testPlatform = "EKS"
	platformGKE   testPlatform = "GKE"
	platformAKS   testPlatform = "AKS"
)

// vmPlatforms are the platforms whose clouds run workloads on VMs.
var vmPlatforms = []testPlatform{platformAWSVM, platformGCPVM}

// Platform returns the cloud's test platform, or a descriptive "PROVIDER/STACK" value for a
// combination no test targets.
func (c CloudInfo) Platform() testPlatform {
	vm := c.IsVM()
	switch {
	case c.Provider == "AWS" && vm:
		return platformAWSVM
	case c.Provider == "GCP" && vm:
		return platformGCPVM
	case c.Provider == "AWS":
		return platformEKS
	case c.Provider == "GCP":
		return platformGKE
	case c.Provider == "AZURE" && !vm:
		return platformAKS
	}
	return testPlatform(c.Provider + "/" + c.ComputeStack)
}

// pinnedCloud returns the pinned test cloud (GetTestCloudID: ANYSCALE_TEST_CLOUD_ID,
// ANYSCALE_TEST_CLOUD_NAME, then the static fixture by name) as a CloudInfo, for tests that
// deploy real workloads onto it. It deliberately does not take the first entry of a cloud
// list: that order is the org's cloud list order, so any other cloud in the shared org -
// another run's tfacc cloud, a hand-made fixture - could win and receive the deployment. The
// test fails if the pinned cloud is not a configured cloud. A test that runs only on some
// platforms calls skipUnlessPlatform with the result.
func pinnedCloud(t *testing.T) CloudInfo {
	t.Helper()
	id := GetTestCloudID(t)
	cloud, ok := selectCloudByID(GetAllConfiguredClouds(t), id)
	if !ok {
		t.Fatalf("pinned test cloud %s is not among the configured clouds with a cloud resource", id)
	}
	return cloud
}

// pinnedVMCloud is pinnedCloud for tests that need a VM cloud: on any other platform the test
// skips, naming the platform.
func pinnedVMCloud(t *testing.T) CloudInfo {
	t.Helper()
	cloud := pinnedCloud(t)
	skipUnlessPlatform(t, cloud, vmPlatforms...)
	return cloud
}

// skipUnlessPlatform skips the test, naming the cloud and its platform, unless the cloud is on
// one of platforms. It never fails: a test that does not apply to the selected fixture is not
// a provider defect.
func skipUnlessPlatform(t skipT, cloud CloudInfo, platforms ...testPlatform) {
	t.Helper()
	got := cloud.Platform()
	for _, p := range platforms {
		if p == got {
			return
		}
	}
	t.Skipf("SKIP(platform): pinned cloud %s is %s; this test runs on %v", cloud.Name, got, platforms)
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

// platformRecorder records whether skipUnlessPlatform skipped.
type platformRecorder struct{ skipped string }

func (r *platformRecorder) Helper() {}
func (r *platformRecorder) Skipf(format string, args ...any) {
	r.skipped = fmt.Sprintf(format, args...)
}

func TestCloudInfoPlatform(t *testing.T) {
	for _, tc := range []struct {
		provider, stack string
		want            testPlatform
	}{
		{"AWS", "VM", platformAWSVM},
		{"AWS", "", platformAWSVM},
		{"GCP", "VM", platformGCPVM},
		{"AWS", "K8S", platformEKS},
		{"GCP", "K8S", platformGKE},
		{"AZURE", "K8S", platformAKS},
		{"AZURE", "VM", "AZURE/VM"},
	} {
		if got := (CloudInfo{Provider: tc.provider, ComputeStack: tc.stack}).Platform(); got != tc.want {
			t.Errorf("%s/%s: Platform() = %q, want %q", tc.provider, tc.stack, got, tc.want)
		}
	}
}

// A VM-only test pointed at a Kubernetes fixture skips, naming the platform; on a VM fixture it
// runs. skipUnlessPlatform has no failure path, so a skip is the only alternative to running.
func TestSkipUnlessPlatform(t *testing.T) {
	eks := CloudInfo{Name: "tfp-test-eks-STATIC", Provider: "AWS", ComputeStack: "K8S"}
	r := &platformRecorder{}
	skipUnlessPlatform(r, eks, vmPlatforms...)
	if !strings.Contains(r.skipped, "SKIP(platform)") || !strings.Contains(r.skipped, "EKS") {
		t.Errorf("EKS fixture with a VM-only test: skip = %q, want a SKIP(platform) naming EKS", r.skipped)
	}

	for _, vm := range []CloudInfo{{Provider: "AWS", ComputeStack: "VM"}, {Provider: "GCP", ComputeStack: "VM"}} {
		r := &platformRecorder{}
		skipUnlessPlatform(r, vm, vmPlatforms...)
		if r.skipped != "" {
			t.Errorf("%s VM fixture with a VM-only test skipped: %q", vm.Provider, r.skipped)
		}
	}

	r = &platformRecorder{}
	skipUnlessPlatform(r, CloudInfo{Provider: "AZURE", ComputeStack: "K8S"}, platformAKS)
	if r.skipped != "" {
		t.Errorf("AKS fixture with an AKS test skipped: %q", r.skipped)
	}
}

// Every platform's fixture cloud must survive the resolver's provider filter, or pinnedCloud
// fails to find it. Azure is Kubernetes-only.
func TestIsKnownProviderCoversEveryPlatform(t *testing.T) {
	for _, tc := range []struct {
		provider, stack string
		want            bool
	}{
		{"AWS", "VM", true}, {"GCP", "VM", true}, {"AWS", "K8S", true}, {"GCP", "K8S", true},
		{"AZURE", "K8S", true}, {"GENERIC", "K8S", true},
		{"AZURE", "VM", false}, {"GENERIC", "VM", false}, {"Generic", "K8S", false}, {"OTHER", "K8S", false},
	} {
		if got := isKnownProvider(tc.provider, tc.stack); got != tc.want {
			t.Errorf("isKnownProvider(%q, %q) = %t, want %t", tc.provider, tc.stack, got, tc.want)
		}
	}
}
