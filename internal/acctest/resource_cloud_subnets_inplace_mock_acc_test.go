package acctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// subnetsInPlaceServer fronts newMockCloudCreateTimeServer and owns the AWS
// subnet lifecycle, so tests can prove the in-place subnet contract
// (2026-11-09):
//
//   - PUT /api/v2/clouds/{id}/resources is scripted and captured: every
//     request body is parsed and retained, so a test can assert the exact
//     wire shape (subnet_ids and zones always paired, file_storage carried
//     in the same PUT) and that list-form subnet_ids without zones is never
//     sent.
//   - Mirrors the live-proven backend behaviors from the Gate 1 log
//     (cl.../gate1-cloud/gate1.log, real AWS 2026-11-09): 422 extra_forbidden
//     on GET-only fields (s-probe first runs), 400 "subnet IDs do not match
//     zones" for a count mismatch (s3, captured verbatim), positional
//     zone-derivation mis-pairing when zones are omitted (s2), zone-sorted
//     regrouping on the stored arrays (s5), and the active-cluster 400
//     (source-traced in the contract; no live capture exists).
type subnetsInPlaceServer struct {
	mu sync.Mutex
	// Deployment modeled on the base mock's static resource: same VPC, SG,
	// roles; only the subnet arrays (and file_storage) are stateful.
	cloudID       string
	resourceID    string
	resourceName  string
	subnetIDs     []string
	zones         []string
	fileStorage   map[string]any
	putBodies     []string // every resources PUT body, in arrival order
	putStatusBody string   // scripted status body; empty = 200
	method        int      // scripted status; 0 = success
	// requireSubnetZones, when true (the default), fails the PUT with the
	// live-proven 400 when subnet_ids arrive without zones. The mutation
	// proof inverts it.
	requireSubnetZones bool
	// deployed tracks the primary deployment's existence: false hides it
	// from GET /resources entirely (the anyscale_cloud_resource flow removes
	// it at destroy; the anyscale_cloud flow never does).
	deployed bool
	// deriveZonesWhenOmitted, when true, simulates the live s2 behavior: a
	// zones-less PUT is ACCEPTED (200) but zones are derived in
	// describe-subnets response order and stored against the REQUEST order -
	// the positional mis-pairing the provider must make impossible. Off by
	// default: the provider never sends that shape, so reaching this branch
	// at all fails loudly via requireSubnetZones.
	deriveZonesWhenOmitted bool
	base                   *mockCloudCreateTimeServer
}

const subnetsInPlaceListTemplate = `{
	"results": [{
		"name": %q, "is_default": true, "cloud_resource_id": %q,
		"provider": "AWS", "compute_stack": "VM", "region": "us-east-2",
		"aws_config": {
			"vpc_id": "vpc-realct123",
			"subnet_ids": [%s],
			"zones": [%s],
			"security_group_ids": ["sg-realct1"],
			"anyscale_iam_role_id": "arn:aws:iam::123456789012:role/real-crossaccount",
			"cluster_iam_role_id": "arn:aws:iam::123456789012:role/real-cluster-node",
			"external_id": "org_real-external-id-ct"
		},
		"object_storage": {"bucket_name": "s3://real-ct-bucket"},
		"file_storage": %s,
		"created_at": "2026-11-09T23:00:00+00:00",
		"operator_status": null, "operator_status_details": null,
		"connector_status": null, "connector_status_details": null
	}],
	"metadata": {"total": 1, "next_paging_token": null}
}`

func (s *subnetsInPlaceServer) listJSONLocked() string {
	if !s.deployed {
		return `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`
	}
	ids := make([]string, len(s.subnetIDs))
	zones := make([]string, len(s.zones))
	for i, id := range s.subnetIDs {
		ids[i] = fmt.Sprintf("%q", id)
	}
	for i, z := range s.zones {
		zones[i] = fmt.Sprintf("%q", z)
	}
	fs := "null"
	if len(s.fileStorage) > 0 {
		b, _ := json.Marshal(s.fileStorage)
		fs = string(b)
	}
	return fmt.Sprintf(subnetsInPlaceListTemplate, s.resourceName, s.resourceID, strings.Join(ids, ","), strings.Join(zones, ","), fs)
}

func newSubnetsInPlaceServer(t *testing.T) (*httptest.Server, *subnetsInPlaceServer) {
	t.Helper()
	inner, base := newMockCloudCreateTimeServer(t)
	target, err := url.Parse(inner.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	s := &subnetsInPlaceServer{
		cloudID:            base.cloudID,
		resourceID:         "cldrsrc_subnetmock",
		resourceName:       "default",
		subnetIDs:          []string{"subnet-realct1", "subnet-realct2"},
		zones:              []string{"us-east-2a", "us-east-2b"},
		requireSubnetZones: true,
		deployed:           true,
		base:               base,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/clouds/"+s.cloudID+"/remove_resource") && r.Method == http.MethodDelete {
			s.mu.Lock()
			s.deployed = false
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		resourcesSuffix := "/api/v2/clouds/" + s.cloudID + "/resources"
		if r.URL.Path == "/api/v2/clouds/"+s.cloudID+"/add_resource" && r.Method == http.MethodPut {
			// Create carries the initial subnets through the request body's
			// aws_config; record them so the deployment reflects create. The
			// response is answered here rather than proxied: the base mock's
			// add_resource echoes no name, and anyscale_cloud_resource's
			// Create keys its ID off the response's own name.
			s.recordAddResource(r)
			s.mu.Lock()
			defer s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result":{"name":%q,"cloud_resource_id":%q}}`, s.resourceName, s.resourceID)
			return
		}
		if r.URL.Path == resourcesSuffix {
			switch r.Method {
			case http.MethodGet:
				s.mu.Lock()
				defer s.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, s.listJSONLocked())
				return
			case http.MethodPut:
				s.handleResourcesPut(w, r)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, s
}

// recordAddResource pulls the create-time aws_config subnet pairs out of the
// request body. Both create forms carry subnet_ids+zones already paired when
// the map form is configured; the list form sends subnet_ids with no zones.
func (s *subnetsInPlaceServer) recordAddResource(r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	// Restore the body so the reverse proxy can forward it untouched.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body struct {
		AWSConfig *struct {
			SubnetIDs []string `json:"subnet_ids"`
			Zones     []string `json:"zones"`
		} `json:"aws_config"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return
	}
	if body.AWSConfig == nil || len(body.AWSConfig.SubnetIDs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subnetIDs = append([]string{}, body.AWSConfig.SubnetIDs...)
	if len(body.AWSConfig.Zones) == len(body.AWSConfig.SubnetIDs) {
		s.zones = append([]string{}, body.AWSConfig.Zones...)
	} else {
		// List-form create: the real backend derives zones from AWS
		// (describe-subnets order). Live-proven on CREATE to pair
		// correctly, so the mock derives a correct-looking pairing from the
		// fixed known zones of the test subnets.
		s.zones = derivedZonesLocked(body.AWSConfig.SubnetIDs)
	}
}

// derivedZonesLocked is the mock's stand-in for describe_subnets: a fixed
// table for the well-known test subnets, in describe response order.
func derivedZonesLocked(ids []string) []string {
	known := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
		"subnet-newct4":  "us-east-2a",
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if z, ok := known[id]; ok {
			out = append(out, z)
		} else {
			out = append(out, "us-east-2x")
		}
	}
	return out
}

// zoneSortLocked regroups the parallel arrays by zone ascending - the
// live-proven storage order (s5: GET is zone-sorted after both s1 and s2).
func zoneSortLocked(ids, zones []string) ([]string, []string) {
	type pair struct{ id, zone string }
	pairs := make([]pair, len(ids))
	for i := range ids {
		pairs[i] = pair{ids[i], zones[i]}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].zone < pairs[j].zone })
	outIDs := make([]string, len(pairs))
	outZones := make([]string, len(pairs))
	for i, p := range pairs {
		outIDs[i], outZones[i] = p.id, p.zone
	}
	return outIDs, outZones
}

func (s *subnetsInPlaceServer) handleResourcesPut(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 0)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putBodies = append(s.putBodies, string(raw))

	if s.method != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.method)
		_, _ = fmt.Fprint(w, s.putStatusBody)
		return
	}

	var reqs []map[string]any
	if err := json.Unmarshal(raw, &reqs); err != nil || len(reqs) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"detail":"mock: expected a one-element request array"}}`)
		return
	}
	req := reqs[0]

	// Live-proven (gate1.log, first probe run): every GET-only field is
	// rejected with this exact 422 body. The typed request already omits
	// them; this catches the regression of echoing a GET verbatim.
	for _, forbidden := range []string{"created_at", "is_default", "operator_status", "operator_status_details", "connector_status", "connector_status_details"} {
		if _, ok := req[forbidden]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprintf(w, `{"error":{"detail":[{"type":"extra_forbidden","loc":["body",0,%q],"msg":"Extra inputs are not permitted"}],"message":"body.0.%s: Extra inputs are not permitted."}}`, forbidden, forbidden)
			return
		}
	}

	// file_storage rides the same PUT.
	if fs, ok := req["file_storage"].(map[string]any); ok {
		s.fileStorage = fs
	} else if req["file_storage"] == nil {
		s.fileStorage = nil
	}

	aws, _ := req["aws_config"].(map[string]any)
	if aws != nil {
		idsAny, idsOK := aws["subnet_ids"].([]any)
		zonesAny, zonesOK := aws["zones"].([]any)
		if idsOK && len(idsAny) > 0 {
			ids := make([]string, 0, len(idsAny))
			for _, v := range idsAny {
				ids = append(ids, fmt.Sprint(v))
			}
			if !zonesOK || len(zonesAny) == 0 {
				// s2 shape: zones omitted. Either refuse (the provider is
				// never allowed to send this) or mis-pair the way the real
				// backend does on UPDATE.
				if s.requireSubnetZones {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprint(w, `{"error":{"detail":"mock: subnets PUT without zones - the provider must always pair subnet ids with zones (live s2 mis-pairing)"}}`)
					return
				}
				if s.deriveZonesWhenOmitted {
					// The live mis-pairing: THIS mock's describe order is
					// deliberately the REVERSE of what EC2 reports, paired
					// positionally with the request order.
					s.subnetIDs = ids
					s.zones = derivedZonesLocked(ids)
					for i, j := 0, len(s.zones)-1; i < j; i, j = i+1, j-1 {
						s.zones[i], s.zones[j] = s.zones[j], s.zones[i]
					}
					return
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, `{"error":{"detail":"mock: zones omitted"}}`)
				return
			}
			if len(idsAny) != len(zonesAny) {
				// s3, captured verbatim from the live backend.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":{"detail":"Failed to update resource %s in cloud %s: convert cloud resource: subnet IDs do not match zones"}}`, s.resourceID, s.cloudID)
				return
			}
			zones := make([]string, 0, len(zonesAny))
			for _, v := range zonesAny {
				zones = append(zones, fmt.Sprint(v))
			}
			// s5: stored zone-sorted.
			s.subnetIDs, s.zones = zoneSortLocked(ids, zones)
		}
		// Unchanged/absent subnet_ids: backend leaves the stored network
		// info as-is (it cannot be cleared).
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "null")
}

func (s *subnetsInPlaceServer) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.putBodies)
}

func (s *subnetsInPlaceServer) lastPutBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.putBodies) == 0 {
		return ""
	}
	return s.putBodies[len(s.putBodies)-1]
}

// setPutError scripts the next (and all subsequent) resources PUTs.
func (s *subnetsInPlaceServer) setPutError(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.method = status
	s.putStatusBody = body
}

func (s *subnetsInPlaceServer) setLiveSubnets(ids, zones []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subnetIDs = append([]string{}, ids...)
	s.zones = append([]string{}, zones...)
}

// liveSubnetPairs returns the currently stored pairs (the truth a drift
// test wants to see).
func (s *subnetsInPlaceServer) liveSubnetPairs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for i, id := range s.subnetIDs {
		if i < len(s.zones) {
			out[id] = s.zones[i]
		}
	}
	return out
}

// putSubnetPairs parses the aws_config subnet pairs out of a captured PUT
// body, or nil when the PUT carried no subnet_ids.
func putSubnetPairs(body string) map[string]string {
	var reqs []struct {
		AWSConfig *struct {
			SubnetIDs []string `json:"subnet_ids"`
			Zones     []string `json:"zones"`
		} `json:"aws_config"`
		FileStorage map[string]any `json:"file_storage"`
	}
	if err := json.Unmarshal([]byte(body), &reqs); err != nil || len(reqs) != 1 || reqs[0].AWSConfig == nil {
		return nil
	}
	aws := reqs[0].AWSConfig
	if len(aws.SubnetIDs) == 0 {
		return nil
	}
	out := map[string]string{}
	for i, id := range aws.SubnetIDs {
		zone := ""
		if i < len(aws.Zones) {
			zone = aws.Zones[i]
		}
		out[id] = zone
	}
	return out
}

func putHasFileStorageID(body, id string) bool {
	var reqs []struct {
		FileStorage *struct {
			FileStorageID string `json:"file_storage_id"`
		} `json:"file_storage"`
	}
	if err := json.Unmarshal([]byte(body), &reqs); err != nil || len(reqs) != 1 {
		return false
	}
	return reqs[0].FileStorage != nil && strings.Contains(reqs[0].FileStorage.FileStorageID, id)
}

// ─── config helpers ─────────────────────────────────────────────────────────

// subnetsInPlaceCloudConfig builds an anyscale_cloud with the given AWS
// subnet form (map XOR list) and, when fileStorageID is non-empty, a
// file_storage block riding the same cloud.
func subnetsInPlaceCloudConfig(apiURL string, mapForm map[string]string, listForm []string, fileStorageID string) string {
	subnets := ""
	if len(mapForm) > 0 {
		pairs := make([]string, 0, len(mapForm))
		for _, id := range sortedMapKeys(mapForm) {
			pairs = append(pairs, fmt.Sprintf("%q = %q", id, mapForm[id]))
		}
		subnets = "subnet_ids_to_az = {\n      " + strings.Join(pairs, ",\n      ") + "\n    }"
	} else if listForm != nil {
		quoted := make([]string, 0, len(listForm))
		for _, id := range listForm {
			quoted = append(quoted, fmt.Sprintf("%q", id))
		}
		subnets = "subnet_ids = [" + strings.Join(quoted, ", ") + "]"
	}

	fileStorage := ""
	if fileStorageID != "" {
		fileStorage = fmt.Sprintf("\n  file_storage {\n    file_storage_id = %q\n  }", fileStorageID)
	}

	return testAccProviderBlock(apiURL) + fmt.Sprintf(`
resource "anyscale_cloud" "test" {
  name           = "createtime-mock"
  cloud_provider = "AWS"
  compute_stack  = "VM"
  region         = "us-east-2"
%s

  aws_config {
    vpc_id                      = "vpc-realct123"
    security_group_ids          = ["sg-realct1"]
    controlplane_iam_role_arn   = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn      = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id                 = "org_real-external-id-ct"

    %s
  }

  object_storage {
    bucket_name = "real-ct-bucket"
  }
}
`, fileStorage, subnets)
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ─── U1: map-form change updates in place ───────────────────────────────────

// TestAccCloudResource_SubnetsToAZChangeUpdatesInPlace proves the headline
// behavior: changing subnet_ids_to_az plans an Update - never a Replace - and
// Update issues exactly one resources PUT whose subnet_ids and zones are the
// map's own pairs. The zones are explicit per the live s2 finding: derived
// zones get paired positionally with the request order and mis-pair.
func TestAccCloudResource_SubnetsToAZChangeUpdatesInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	three := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
	}
	two := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct3": "us-east-2c",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, three, nil, "")},
			{
				Config: subnetsInPlaceCloudConfig(server.URL, two, nil, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
				// The apply's own state comes from the plan; the refreshed
				// map (built from the mock's zone-sorted arrays) must agree
				// with the configured pairs.
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.subnet_ids_to_az.subnet-realct1", "us-east-2a"),
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.subnet_ids_to_az.subnet-realct3", "us-east-2c"),
				),
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Errorf("resources PUTs across the whole test = %d, want exactly 1 (the map-form change)", got)
	}
	pairs := putSubnetPairs(mock.lastPutBody())
	if len(pairs) != 2 || pairs["subnet-realct1"] != "us-east-2a" || pairs["subnet-realct3"] != "us-east-2c" {
		t.Errorf("PUT carried subnet pairs %v, want the map's own explicit pairs", pairs)
	}
	// The mock stored them zone-sorted; the live truth must equal the config.
	for id, zone := range two {
		if mock.liveSubnetPairs()[id] != zone {
			t.Errorf("mock live pairing %s=%q after the PUT, want %q", id, mock.liveSubnetPairs()[id], zone)
		}
	}
}

// ─── U2: list-form reorder is a no-op ───────────────────────────────────────

// TestAccCloudResource_SubnetIDsReorderIsEmptyPlan: reordering the same set of
// subnets through the list form produces NO plan action and no PUT. Element
// order never reaches the backend (the resources PUT regroups by zone), so the
// plan modifier normalizes the planned order to the state's order.
func TestAccCloudResource_SubnetIDsReorderIsEmptyPlan(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	forward := []string{"subnet-realct1", "subnet-realct2", "subnet-realct3"}
	reversed := []string{"subnet-realct3", "subnet-realct2", "subnet-realct1"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: subnetsInPlaceCloudConfig(server.URL, nil, forward, ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud.test", "aws_config.subnet_ids.#", "3"),
				),
			},
			{
				Config: subnetsInPlaceCloudConfig(server.URL, nil, reversed, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionNoop),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs after reorder = %d, want 0 (a reorder is not a change)", got)
	}
}

// ─── U3: list-form set change is refused at plan time ──────────────────────

// TestAccCloudResource_SubnetIDsSetChangeRefusedAtPlanTime: changing the
// subnet SET through the list form never reaches the API - it errors at plan
// time with the subnet_ids_to_az guidance, because Anyscale derives zones
// itself and can pair them with the wrong subnets (live-proven on UPDATE, s2).
func TestAccCloudResource_SubnetIDsSetChangeRefusedAtPlanTime(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.resourceName = "primary-resource-mock"
	before := []string{"subnet-realct1", "subnet-realct2"}
	changed := []string{"subnet-realct1", "subnet-realct3"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, nil, before, "")},
			{
				// The error fires at plan time; no apply happens for this step.
				Config:      subnetsInPlaceCloudConfig(server.URL, nil, changed, ""),
				ExpectError: regexp.MustCompile(`Changing subnets in place requires aws_config\.subnet_ids_to_az`),
			},
			{
				PreConfig: func() {
					if got := mock.putCount(); got != 0 {
						t.Errorf("resources PUTs after the refused plan = %d, want 0", got)
					}
				},
				// The cloud is untouched; the original config still applies clean.
				Config: subnetsInPlaceCloudConfig(server.URL, nil, before, ""),
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs across the whole test = %d, want 0", got)
	}
}

// List-form drift hits the same error: Read refreshes the live set into state,
// so unchanged config against a drifted list-form cloud plans a set change
// and is refused with the same guidance. The fix is the map form either way.
func TestAccCloudResource_SubnetIDsListFormDriftRefusedWithGuidance(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	configList := []string{"subnet-realct1", "subnet-realct2"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, nil, configList, "")},
			{
				// Out-of-band CLI subnet change on the same set of zones.
				PreConfig: func() {
					mock.setLiveSubnets([]string{"subnet-realct1", "subnet-realct3"}, []string{"us-east-2a", "us-east-2c"})
				},
				Config:      subnetsInPlaceCloudConfig(server.URL, nil, configList, ""),
				ExpectError: regexp.MustCompile(`Changing subnets in place requires aws_config\.subnet_ids_to_az`),
			},
		},
	})
}

// ─── U4: list -> map switch heals mis-paired zones ─────────────────────────

// TestAccCloudResource_SubnetIDsToAZSwitchHealsMisPairing: a list-form cloud
// whose stored zones are paired with the wrong subnets (the live s2 outcome)
// is healed by switching the config to the map form with the SAME subnets and
// the true zones: Update PUTs once - the form switch counts as a change - and
// writes the correct pairs.
func TestAccCloudResource_SubnetIDsToAZSwitchHealsMisPairing(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	list := []string{"subnet-realct1", "subnet-realct2"}
	truth := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, nil, list, "")},
			{
				PreConfig: func() {
					// The live mis-pairing s2 produced: positional derivation
					// stored the zones reversed against these subnet ids.
					mock.setLiveSubnets([]string{"subnet-realct1", "subnet-realct2"},
						[]string{"us-east-2b", "us-east-2a"})
				},
				Config: subnetsInPlaceCloudConfig(server.URL, truth, nil, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Errorf("resources PUTs = %d, want exactly 1 (the form switch writes the true pairs)", got)
	}
	pairs := putSubnetPairs(mock.lastPutBody())
	if len(pairs) != 2 || pairs["subnet-realct1"] != "us-east-2a" || pairs["subnet-realct2"] != "us-east-2b" {
		t.Errorf("PUT carried pairs %v, want the map's true pairs", pairs)
	}
	live := mock.liveSubnetPairs()
	for id, zone := range truth {
		if live[id] != zone {
			t.Errorf("live pairing after heal: %s=%q, want %q", id, live[id], zone)
		}
	}
}

// ─── U5: map-form out-of-band drift reverts on apply ───────────────────────

// TestAccCloudResource_SubnetsToAZDriftShowsUpdateAndReverts: with subnets
// changed out of band (the CLI route this customer used), refresh surfaces the
// drift as an in-place Update, and apply reverts it with exactly one PUT
// carrying the configured pairs - no replace, no ignore_changes workaround.
func TestAccCloudResource_SubnetsToAZDriftShowsUpdateAndReverts(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	orig := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, orig, nil, "")},
			{
				PreConfig: func() {
					// The customer's exact workaround: subnets changed via
					// the Anyscale CLI while Terraform config stood still.
					mock.setLiveSubnets([]string{"subnet-newct4", "subnet-realct2"}, []string{"us-east-2a", "us-east-2b"})
				},
				Config: subnetsInPlaceCloudConfig(server.URL, orig, nil, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Errorf("resources PUTs = %d, want exactly 1 (the revert)", got)
	}
	live := mock.liveSubnetPairs()
	for id, zone := range orig {
		if live[id] != zone {
			t.Errorf("live pairing after revert: %s=%q, want %q", id, live[id], zone)
		}
	}
	if _, still := live["subnet-newct4"]; still {
		t.Errorf("drifted subnet-newct4 still live after the revert: %v", live)
	}
}

// ─── U6: PUT refusals error cleanly, never taint ───────────────────────────

// A count-mismatch 400 (live-captured verbatim, s3) surfaces as an Update
// error; the cloud is not tainted, and the next apply with the error cleared
// proceeds as an ordinary in-place Update.
func TestAccCloudResource_SubnetUpdateCountMismatchErrorsNotTaints(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	three := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
	}
	two := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct3": "us-east-2c",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, three, nil, "")},
			{
				PreConfig: func() {
					mock.setPutError(http.StatusBadRequest,
						fmt.Sprintf(`{"error":{"detail":"Failed to update resource %s in cloud %s: convert cloud resource: subnet IDs do not match zones"}}`, mock.resourceID, mock.cloudID))
				},
				Config:      subnetsInPlaceCloudConfig(server.URL, two, nil, ""),
				ExpectError: regexp.MustCompile(`subnet IDs do not match zones`),
			},
			{
				PreConfig: func() { mock.setPutError(0, "") },
				// NOT a create: the failed Update left the cloud in state
				// untouched, so the retry is the same in-place Update.
				Config: subnetsInPlaceCloudConfig(server.URL, two, nil, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 2 {
		t.Errorf("resources PUTs = %d, want 2 (one refused, one retry)", got)
	}
}

// The active-cluster refusal (source-traced from the backend, no live capture)
// keeps its designed translation and likewise never taints.
func TestAccCloudResource_SubnetUpdateActiveClusterErrorsNotTaints(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	three := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
	}
	two := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct3": "us-east-2c",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, three, nil, "")},
			{
				PreConfig: func() {
					// Source-traced (src: _validate_aws_subnets - no live
					// capture exists for this one); the mock body is the
					// backend's own error shape.
					mock.setPutError(http.StatusBadRequest,
						`{"error":{"detail":"Existing resources can not be updated while there are active clusters using the resource."}}`)
				},
				Config:      subnetsInPlaceCloudConfig(server.URL, two, nil, ""),
				ExpectError: regexp.MustCompile(`Cannot Update the Cloud While Clusters Are Running`),
			},
			{
				PreConfig: func() { mock.setPutError(0, "") },
				Config:    subnetsInPlaceCloudConfig(server.URL, two, nil, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// ─── U8: subnet and file_storage changes share one PUT ─────────────────────

// TestAccCloudResource_SubnetAndFileStorageUpdateTogether: when both the
// subnets and file_storage change, Update issues exactly ONE resources PUT
// carrying both - the same request the CLI's cloud update sends.
func TestAccCloudResource_SubnetAndFileStorageUpdateTogether(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	three := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
	}
	two := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct3": "us-east-2c",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudConfig(server.URL, three, nil, "")},
			{
				Config: subnetsInPlaceCloudConfig(server.URL, two, nil, "fs-new-test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Errorf("resources PUTs = %d, want exactly 1 carrying BOTH subnets and file_storage", got)
	}
	body := mock.lastPutBody()
	pairs := putSubnetPairs(body)
	if len(pairs) != 2 || pairs["subnet-realct1"] != "us-east-2a" || pairs["subnet-realct3"] != "us-east-2c" {
		t.Errorf("PUT carried subnet pairs %v, want the map's pairs", pairs)
	}
	if !putHasFileStorageID(body, "fs-new-test") {
		t.Errorf("PUT body does not carry the changed file_storage id:\n%s", body)
	}
}

// ─── anyscale_cloud_resource wiring smoke ───────────────────────────────────

// The subnet in-place machinery is shared with anyscale_cloud_resource, whose
// Update and Read wiring are distinct code paths - these two smoke tests prove
// the same contract holds on the second resource with the same mocked backend.

// subnetsInPlaceCloudResourceConfig renders an anyscale_cloud (empty of
// embedded config, so the cloud resource owns the deployment) plus one
// anyscale_cloud_resource carrying the given subnet form.
func subnetsInPlaceCloudResourceConfig(serverURL string, cloudID string, mapForm map[string]string, listForm []string) string {
	subnets := ""
	if len(mapForm) > 0 {
		pairs := make([]string, 0, len(mapForm))
		for _, id := range sortedMapKeys(mapForm) {
			pairs = append(pairs, fmt.Sprintf("%q = %q", id, mapForm[id]))
		}
		subnets = "subnet_ids_to_az = {\n      " + strings.Join(pairs, ",\n      ") + "\n    }"
	} else if listForm != nil {
		quoted := make([]string, 0, len(listForm))
		for _, id := range listForm {
			quoted = append(quoted, fmt.Sprintf("%q", id))
		}
		subnets = "subnet_ids = [" + strings.Join(quoted, ", ") + "]"
	}
	return testAccProviderBlock(serverURL) + fmt.Sprintf(`
resource "anyscale_cloud" "parent" {
  cloud_provider = "AWS"
  region         = "us-east-2"
  name           = "createtime-mock"
}

resource "anyscale_cloud_resource" "test" {
  cloud_id      = %q
  name          = "primary-resource-mock"
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                      = "vpc-realct123"
    security_group_ids          = ["sg-realct1"]
    controlplane_iam_role_arn   = "arn:aws:iam::123456789012:role/real-crossaccount"
    dataplane_iam_role_arn      = "arn:aws:iam::123456789012:role/real-cluster-node"
    external_id                 = "org_real-external-id-ct"

    %s
  }
}
`, cloudID, subnets)
}

func TestAccCloudResourceResource_SubnetsToAZChangeUpdatesInPlace(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.resourceName = "primary-resource-mock"
	three := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct2": "us-east-2b",
		"subnet-realct3": "us-east-2c",
	}
	two := map[string]string{
		"subnet-realct1": "us-east-2a",
		"subnet-realct3": "us-east-2c",
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudResourceConfig(server.URL, mock.cloudID, three, nil)},
			{
				Config: subnetsInPlaceCloudResourceConfig(server.URL, mock.cloudID, two, nil),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("anyscale_cloud_resource.test", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
	if got := mock.putCount(); got != 1 {
		t.Errorf("resources PUTs = %d, want exactly 1", got)
	}
	pairs := putSubnetPairs(mock.lastPutBody())
	if len(pairs) != 2 || pairs["subnet-realct1"] != "us-east-2a" || pairs["subnet-realct3"] != "us-east-2c" {
		t.Errorf("PUT carried pairs %v, want the map's explicit pairs", pairs)
	}
}

func TestAccCloudResourceResource_SubnetIDsSetChangeRefusedAtPlanTime(t *testing.T) {
	SkipIfNotAcceptanceTest(t)
	server, mock := newSubnetsInPlaceServer(t)
	mock.resourceName = "primary-resource-mock"
	before := []string{"subnet-realct1", "subnet-realct2"}
	changed := []string{"subnet-realct1", "subnet-realct3"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: subnetsInPlaceCloudResourceConfig(server.URL, mock.cloudID, nil, before)},
			{
				Config:      subnetsInPlaceCloudResourceConfig(server.URL, mock.cloudID, nil, changed),
				ExpectError: regexp.MustCompile(`Changing subnets in place requires aws_config\.subnet_ids_to_az`),
			},
		},
	})
	if got := mock.putCount(); got != 0 {
		t.Errorf("resources PUTs = %d, want 0 (the refusal must fire before any apply)", got)
	}
}
