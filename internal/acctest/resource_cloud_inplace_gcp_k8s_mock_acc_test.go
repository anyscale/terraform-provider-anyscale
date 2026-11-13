package acctest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// In-place update coverage for the GCP and Kubernetes blocks against a
// live-shaped mock, per docs/decisions/cloud-resource-inplace-update/README.md:
//   - the K8S lossless echo: a live kubernetes_config.custom_image_registry -
//     a field this provider's schema does not model - must ride through a
//     zones update unchanged, because the backend clears any field a sent
//     block leaves out, so an update that drops it would wipe it;
//   - a GCP block-level change that applies in place;
//   - a null, unmanaged state attribute is never adopted from the live
//     record, so the plan stays empty;
//   - removing a value the backend cannot actually clear (subnet_names, the
//     K8S operator identity, or a whole kubernetes_config / object_storage
//     block) is refused at plan time.
type inplaceMixedServer struct {
	mu           sync.Mutex
	cloudID      string
	resourceID   string
	resourceName string
	computeStack string // "VM" (GCP) or "K8S"

	// GCP VM deployment state.
	subnetNames                 []string
	firewallPolicyNames         []string
	dataplaneServiceAccountMail string

	// K8S deployment state. customImageRegistry is the unmodeled live field
	// the lossless echo must carry back.
	zones               []string
	operatorIdentity    string
	customImageRegistry string

	// objectStorage is rendered verbatim; tests may seed an unmodeled key.
	objectStorage map[string]any

	putBodies []string
	deployed  bool
}

func (s *inplaceMixedServer) listJSONLocked() string {
	if !s.deployed {
		return `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`
	}
	os := "null"
	if s.objectStorage != nil {
		b, _ := json.Marshal(s.objectStorage)
		os = string(b)
	}
	quote := func(items []string) string {
		quoted := make([]string, len(items))
		for i, v := range items {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		return "[" + strings.Join(quoted, ",") + "]"
	}
	block := ""
	switch s.computeStack {
	case "K8S":
		block = fmt.Sprintf(`"kubernetes_config": {
			"anyscale_operator_iam_identity": %q,
			"zones": %s,
			"redis_endpoint": "",
			"custom_image_registry": %q
		}`, s.operatorIdentity, quote(s.zones), s.customImageRegistry)
	default: // GCP VM
		block = fmt.Sprintf(`"gcp_config": {
			"project_id": "my-gcp-project",
			"provider_name": "provider-name",
			"vpc_name": "vpc-inplace-mock",
			"subnet_names": %s,
			"firewall_policy_names": %s,
			"anyscale_service_account_email": "controlplane@my-gcp-project.iam.gserviceaccount.com",
			"cluster_service_account_email": %q,
			"deployment_manager_id": null,
			"infrastructure_manager_id": null
		}`, quote(s.subnetNames), quote(s.firewallPolicyNames), s.dataplaneServiceAccountMail)
	}
	return fmt.Sprintf(`{
	"results": [{
		"name": %q, "is_default": true, "cloud_resource_id": %q,
		"provider": "GCP", "compute_stack": %q, "region": "us-central1",
		%s,
		"object_storage": %s,
		"file_storage": null,
		"created_at": "2026-11-12T23:00:00+00:00",
		"operator_status": null, "operator_status_details": null
	}],
	"metadata": {"total": 1, "next_paging_token": null}}`, s.resourceName, s.resourceID, s.computeStack, block, os)
}

func newInplaceMixedServer(t *testing.T, computeStack string) (*httptest.Server, *inplaceMixedServer) {
	t.Helper()
	inner, base := newMockCloudCreateTimeServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	s := &inplaceMixedServer{
		cloudID:      base.cloudID,
		resourceID:   "cldrsrc_inplacemixed",
		resourceName: "default",
		computeStack: computeStack,
		deployed:     true,
	}
	switch computeStack {
	case "K8S":
		s.operatorIdentity = "inplace-operator@my-gcp-project.iam.gserviceaccount.com"
		s.zones = []string{"us-central1-a"}
		s.customImageRegistry = "us-docker.pkg.dev/my-gcp-project/anyscale"
	default:
		s.subnetNames = []string{"subnet-gcp-one", "subnet-gcp-two"}
		s.firewallPolicyNames = []string{"fw-existing"}
		s.dataplaneServiceAccountMail = "dataplane@my-gcp-project.iam.gserviceaccount.com"
	}
	s.objectStorage = map[string]any{"bucket_name": "gs://inplace-mixed-bucket"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/clouds/"+s.cloudID+"/remove_resource") && r.Method == http.MethodDelete {
			s.mu.Lock()
			s.deployed = false
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// The base create-time mock answers the cloud GET with a hardcoded
		// AWS us-east-2 record; this deployment is GCP us-central1, so the
		// cloud response is overridden the same way the resources one is.
		if r.URL.Path == "/api/v2/clouds/"+s.cloudID && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{
				"result": {"id": %q, "name": "createtime-mock", "provider": "GCP", "region": "us-central1",
				"status": "ready", "state": "ACTIVE", "compute_stack": %q, "is_default": false,
				"is_private_cloud": false, "auto_add_user": false, "lineage_tracking_enabled": false,
				"is_aggregated_logs_enabled": false, "creator_id": "usr_mock", "credentials": "fake",
				"created_at": "2026-11-12T23:00:00+00:00", "version": "1"}
			}`, s.cloudID, s.computeStack)
			return
		}
		resourcesSuffix := "/api/v2/clouds/" + s.cloudID + "/resources"
		if r.URL.Path == resourcesSuffix && r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			s.mu.Lock()
			s.putBodies = append(s.putBodies, string(raw))
			var reqs []map[string]any
			if err := json.Unmarshal(raw, &reqs); err == nil && len(reqs) == 1 {
				req := reqs[0]
				// The backend assigns every field of a SENT block
				// unconditionally; omitting a key clears it.
				if gcp, ok := req["gcp_config"].(map[string]any); ok {
					if v, ok := gcp["subnet_names"].([]any); ok {
						s.subnetNames = toStringSlice(v)
					} else {
						s.subnetNames = nil
					}
					if v, ok := gcp["firewall_policy_names"].([]any); ok {
						s.firewallPolicyNames = toStringSlice(v)
					} else {
						s.firewallPolicyNames = nil
					}
					if v, ok := gcp["cluster_service_account_email"].(string); ok {
						s.dataplaneServiceAccountMail = v
					}
				}
				if k8s, ok := req["kubernetes_config"].(map[string]any); ok {
					if v, ok := k8s["zones"].([]any); ok {
						s.zones = toStringSlice(v)
					} else {
						s.zones = nil
					}
					if v, ok := k8s["anyscale_operator_iam_identity"].(string); ok {
						s.operatorIdentity = v
					}
				}
				if osBlock, ok := req["object_storage"].(map[string]any); ok {
					s.objectStorage = osBlock
				}
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "null")
			return
		}
		if r.URL.Path == resourcesSuffix && r.Method == http.MethodGet {
			s.mu.Lock()
			defer s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, s.listJSONLocked())
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, s
}

func toStringSlice(v []any) []string {
	out := make([]string, 0, len(v))
	for _, item := range v {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func (s *inplaceMixedServer) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.putBodies)
}

func (s *inplaceMixedServer) lastPutBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.putBodies) == 0 {
		return ""
	}
	return s.putBodies[len(s.putBodies)-1]
}

func (s *inplaceMixedServer) liveZones() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.zones...)
}

// putBlock parses the last PUT body's named block object, or nil.
func putBlock(body, block string) map[string]any {
	var reqs []struct {
		GCP          map[string]any `json:"gcp_config"`
		Kubernetes   map[string]any `json:"kubernetes_config"`
		ObjectStorag map[string]any `json:"object_storage"`
	}
	if json.Unmarshal([]byte(body), &reqs) != nil || len(reqs) != 1 {
		return nil
	}
	switch block {
	case "gcp_config":
		return reqs[0].GCP
	case "kubernetes_config":
		return reqs[0].Kubernetes
	default:
		return reqs[0].ObjectStorag
	}
}

// ─── K8S: a zones change is a lossless echo of the live block ──────────────

// TestAccCloudResource_InPlaceKubernetesZonesEchoUnmodeledRegistry proves the
// K8S echo is lossless: a live kubernetes_config.custom_image_registry
// (unmodeled by this provider's schema) must ride through a zones update
// unchanged, and the backend must end up storing the new zones.
func TestAccCloudResource_InPlaceKubernetesZonesEchoUnmodeledRegistry(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "K8S")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inplaceK8SConfig(server.URL, `"us-central1-a"`)},
			{
				Config: inplaceK8SConfig(server.URL, `"us-central1-a", "us-central1-b"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: inplaceK8SConfig(server.URL, `"us-central1-a", "us-central1-b"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the zones change)", got)
	}
	k8s := putBlock(mock.lastPutBody(), "kubernetes_config")
	if k8s == nil {
		t.Fatal("PUT carried no kubernetes_config despite the zones change")
	}
	if k8s["custom_image_registry"] != "us-docker.pkg.dev/my-gcp-project/anyscale" {
		t.Errorf("PUT kubernetes_config.custom_image_registry = %v, want the live value carried back", k8s["custom_image_registry"])
	}
	if fmt.Sprint(k8s["zones"]) != "[us-central1-a us-central1-b]" {
		t.Errorf("PUT zones = %v, want the plan's two zones", k8s["zones"])
	}
	if got := mock.liveZones(); len(got) != 2 {
		t.Errorf("mock stored zones %v after the PUT, want both zones", got)
	}
}

// ─── K8S: removing values the backend cannot clear is refused ───────────────

// TestAccCloudResource_InPlaceKubernetesRemovalsRefused: removing the K8S
// operator identity, the whole kubernetes_config block, or the whole
// object_storage block fails the plan - every attribute in both is in place,
// so a null block would be a silently skipped, never-converging update.
func TestAccCloudResource_InPlaceKubernetesRemovalsRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "K8S")
	base := inplaceK8SConfig(server.URL, `"us-central1-a"`)
	noOperator := strings.Replace(base,
		`    anyscale_operator_iam_identity = "inplace-operator@my-gcp-project.iam.gserviceaccount.com"`, "", 1)
	noKubeBlock := strings.Replace(base, `
  kubernetes_config {
    anyscale_operator_iam_identity = "inplace-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = ["us-central1-a"]
  }`, "", 1)
	noObjectStorage := strings.Replace(base, `
  object_storage {
    bucket_name = "inplace-mixed-bucket"
  }`, "", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:             noOperator,
				ExpectError:        regexp.MustCompile("(?s)Cannot Remove anyscale_operator_iam_identity"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config:             noKubeBlock,
				ExpectError:        regexp.MustCompile("(?s)Cannot Remove the kubernetes_config Block"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config:             noObjectStorage,
				ExpectError:        regexp.MustCompile("(?s)Cannot Remove the object_storage Block"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after plan-time refusals = %d, want 0", got)
	}
}

// ─── GCP: a firewall-policy change updates in place, losslessly ────────────

func TestAccCloudResource_InPlaceGCPFirewallPolicyChange(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "VM")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: inplaceGCPConfig(server.URL, `"fw-existing"`)},
			{
				Config: inplaceGCPConfig(server.URL, `"fw-existing", "fw-new"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: inplaceGCPConfig(server.URL, `"fw-existing", "fw-new"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Fatalf("resources PUTs = %d, want exactly 1 (the firewall-policy change)", got)
	}
	gcp := putBlock(mock.lastPutBody(), "gcp_config")
	if gcp == nil {
		t.Fatal("PUT carried no gcp_config despite the change")
	}
	if fmt.Sprint(gcp["firewall_policy_names"]) != "[fw-existing fw-new]" {
		t.Errorf("PUT firewall_policy_names = %v, want the plan's policies", gcp["firewall_policy_names"])
	}
	if fmt.Sprint(gcp["subnet_names"]) != "[subnet-gcp-one subnet-gcp-two]" {
		t.Errorf("PUT subnet_names = %v, want the unchanged live subnets carried", gcp["subnet_names"])
	}
	if got := len(mock.liveZones()); got != 0 {
		_ = got // GCP VM: zones are not part of this deployment
	}
}

// ─── GCP: a null, unmanaged attribute is never adopted ────────────────────

// TestAccCloudResource_InPlaceGCPUnmanagedStaysNull proves the null-state
// skip from the other side: the live record carries firewall_policy_names,
// but the config never declared it, so state stays null and the plan stays
// empty - the refresh must not adopt the live value as a perpetual diff.
func TestAccCloudResource_InPlaceGCPUnmanagedStaysNull(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "VM")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// No firewall_policy_names declared; the live record carries
				// fw-existing.
				Config: inplaceGCPConfig(server.URL, `__OMITTED__`),
				Check:  resource.TestCheckNoResourceAttr("anyscale_cloud.test", "gcp_config.0.firewall_policy_names"),
			},
			{
				// The refresh must not adopt the live policies into the null
				// state: the plan stays empty.
				Config:   inplaceGCPConfig(server.URL, `__OMITTED__`),
				PlanOnly: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (nothing to adopt)", got)
	}
}

// TestAccCloudResource_InPlaceGCPSubnetNamesRemovalRefused: removing GCP
// subnet_names is refused at plan time because the backend keeps the old
// subnets when the sent block is empty, so the removal would otherwise plan
// 'to null' while nothing clears - a silent lie.
func TestAccCloudResource_InPlaceGCPSubnetNamesRemovalRefused(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newInplaceMixedServer(t, "VM")
	base := inplaceGCPConfig(server.URL, `"fw-existing"`)
	noSubnets := strings.Replace(base, `subnet_names                  = ["subnet-gcp-one", "subnet-gcp-two"]`, "", 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:             noSubnets,
				ExpectError:        regexp.MustCompile("(?s)Cannot Remove subnet_names"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after the refusal = %d, want 0", got)
	}
}

func inplaceK8SConfig(apiURL, zones string) string {
	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "GCP"
  compute_stack  = "K8S"
  region         = "us-central1"

  kubernetes_config {
    anyscale_operator_iam_identity = "inplace-operator@my-gcp-project.iam.gserviceaccount.com"
    zones                          = [%s]
  }

  object_storage {
    bucket_name = "inplace-mixed-bucket"
  }
}
`, zones)
}

func inplaceGCPConfig(apiURL, firewallPolicies string) string {
	firewall := ""
	if firewallPolicies != "" && firewallPolicies != "__OMITTED__" {
		firewall = fmt.Sprintf("\n    firewall_policy_names = [%s]", firewallPolicies)
	}
	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "GCP"
  compute_stack  = "VM"
  region         = "us-central1"

  gcp_config {
    project_id                    = "my-gcp-project"
    provider_name                 = "provider-name"
    vpc_name                      = "vpc-inplace-mock"
    subnet_names                  = ["subnet-gcp-one", "subnet-gcp-two"]%s
    controlplane_service_account_email = "controlplane@my-gcp-project.iam.gserviceaccount.com"
    dataplane_service_account_email    = "dataplane@my-gcp-project.iam.gserviceaccount.com"
  }

  object_storage {
    bucket_name = "inplace-mixed-bucket"
  }
}
`, firewall)
}
