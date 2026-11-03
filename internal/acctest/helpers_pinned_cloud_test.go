package acctest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
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
	client, err := GetTestClient()
	if err != nil {
		t.Fatalf("failed to get test client: %v", err)
	}
	cloud, err := configuredCloudByID(client, id)
	if err != nil {
		t.Fatalf("pinned test cloud %s: %v", id, err)
	}
	return cloud
}

// configuredCloudByID looks the cloud up by ID, so it is found however many clouds the org has,
// and fails unless it is on a provider the tests target and has a cloud resource.
func configuredCloudByID(client *provider.Client, id string) (CloudInfo, error) {
	resp, err := provider.DoRequestAndParse[struct {
		Result listedCloud `json:"result"`
	}](context.Background(), client, "GET", "/api/v2/clouds/"+id, nil, http.StatusOK)
	if err != nil {
		return CloudInfo{}, err
	}
	c := resp.Result
	stack := normalizeComputeStack(c.ComputeStack)
	if !isKnownProvider(c.Provider, stack) {
		return CloudInfo{}, fmt.Errorf("cloud %s is on %s/%s, which no test targets", c.Name, c.Provider, stack)
	}
	hasResources, definitive := cloudHasResources(client, id)
	if !definitive {
		return CloudInfo{}, fmt.Errorf("could not determine whether cloud %s has a cloud resource", c.Name)
	}
	if !hasResources {
		return CloudInfo{}, fmt.Errorf("cloud %s has no cloud resource", c.Name)
	}
	return CloudInfo{ID: c.ID, Name: c.Name, Provider: c.Provider, ComputeStack: stack}, nil
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

// The pinned cloud is looked up by ID: found regardless of where it would sit in the org's cloud
// list, and rejected when no test can use it.
func TestConfiguredCloudByID(t *testing.T) {
	for _, tc := range []struct {
		name, cloud, resources string
		status                 int
		wantPlatform           testPlatform
		wantErr                string
	}{
		{"EKS fixture", `{"id": "cld_x", "name": "eks", "provider": "AWS", "compute_stack": "K8S"}`, `[{"id": "r"}]`, 200, platformEKS, ""},
		{"AKS fixture", `{"id": "cld_x", "name": "aks", "provider": "AZURE", "compute_stack": "K8S"}`, `[{"id": "r"}]`, 200, platformAKS, ""},
		{"AWS VM fixture", `{"id": "cld_x", "name": "vm", "provider": "AWS", "compute_stack": "VM"}`, `[{"id": "r"}]`, 200, platformAWSVM, ""},
		{"no cloud resource", `{"id": "cld_x", "name": "empty", "provider": "AWS", "compute_stack": "VM"}`, `[]`, 200, "", "has no cloud resource"},
		{"untargeted provider", `{"id": "cld_x", "name": "odd", "provider": "AZURE", "compute_stack": "VM"}`, `[{"id": "r"}]`, 200, "", "no test targets"},
		{"cloud missing", `{}`, `[]`, 404, "", "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/clouds/cld_x":
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprintf(w, `{"result": %s}`, tc.cloud)
				case "/api/v2/clouds/cld_x/resources":
					_, _ = fmt.Fprintf(w, `{"results": %s}`, tc.resources)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			got, err := configuredCloudByID(provider.NewClientWithToken(server.URL, "pinned-cloud-fake-token"), "cld_x")
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			case tc.wantErr == "" && got.Platform() != tc.wantPlatform:
				t.Fatalf("platform = %s, want %s", got.Platform(), tc.wantPlatform)
			}
		})
	}
}
