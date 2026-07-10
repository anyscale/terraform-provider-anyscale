package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/anyscale/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// This file is the permanent CI-gated regression harness for CR1 (design doc
// CLOUD-RESOURCE-DESIGN.md, "MATRIX LOCKED" -> Option B, REVERSED to "A2" on
// 2026-07-08 after a real E2E against multi-resource-cloud-basic surfaced a
// second, independent backend defect -- see CLOUD-RESOURCE-REPORT.md S7):
// two anyscale_cloud_resource blocks attached to one anyscale_cloud used to
// be silently mergeable into a single backend resource, because Create()
// computed a resource's name client-side and then unconditionally
// overwrote a second block's name with an already-existing default
// resource's name whenever the cloud had exactly one (resource_cloud_resource.go,
// pre-fix Create() ~L689-714 + findDefaultCloudResource ~L976-992). That part
// of the diagnosis never changed across B and A2 -- both fixes remove the
// adopt path. What changed is how a resource's name is chosen when a config
// block omits it.
//
// Option B (superseded): drop client-side name computation entirely, always
// send an empty name when omitted, and let the backend own naming
// (auto-generate + auto-suffix on collision, cloud_resources_dao.py's
// _generate_cloud_resource_name). This closed CR1 in the mock but was never
// exercised against real infra until the multi-resource-cloud-basic E2E: the
// real add_cloud_resource handler (clouds_resource.py add_cloud_resource,
// ~L2460-2570) discards the DAO's created-row return and responds with the
// ORIGINAL REQUEST object instead -- so when the request's name is empty,
// the response's name is empty too, even though the DAO generated and
// persisted a real one. Nothing client-side or mock-side could see that seam
// -- it's a real-backend-only bug in response construction, independent of
// the DAO's own (still believed-correct, never disproven) generation logic.
// This broke the COMMON case (a single resource, name omitted), which B had
// no coverage for because every acceptance criterion in the original matrix
// was about the multi-resource scenario.
//
// A2 (current, architect DECISION 2026-07-08): restore the exact client-side
// name computation -- {compute_stack}-{provider}-{region}, lowercased --
// that B had deleted, so the provider ALWAYS sends a concrete, non-empty
// name and never depends on reading a generated name back. Keep the adopt
// path removed (CR1 stays fixed). Deliberately NO client-side auto-suffix on
// a tuple collision ("do not replicate the backend counter -- race-prone,"
// architect) -- a second resource sharing the same region/compute_stack/
// provider as an existing one now needs an EXPLICIT distinct name, or the
// backend's real unique index on (cloud_id, name) 409s, loudly, every time.
// This is the ORIGINAL working behavior (pre-regression) plus the CR1 fix;
// release impact is unchanged (PATCH).
//
// The mock below is built to the TRACED real backend behavior for BOTH
// branches: a duplicate explicit name always 409s (create-only, DB unique
// index -- never an upsert), and an add_resource response echoes the
// REQUEST's name verbatim -- including empty, when a caller sends empty --
// never the DAO's internally generated one. That second property is a
// deliberate regression tripwire: A2 means the provider should never again
// send an empty name, but if it ever does, this mock now reproduces the
// exact real "the name never comes back" failure instead of the
// too-helpful auto-return an earlier version of this file used, so a
// regression fails loudly here instead of surfacing only against real infra
// again.
//
// STATUS (updated 2026-07-08, Required-name): architect superseded A2's
// client-side name computation with a plan-time Required-name design --
// name flips from Optional+Computed to Required:true, and the client-side
// {compute_stack}-{provider}-{region} default block described above is
// deleted from Create() entirely (forge, da7c125). A same-day follow-up
// closes a second gap Required alone left open: Required:true stops an
// OMITTED name but not an explicitly EMPTY one, since "" is a valid
// "present" value -- so name also gains a stringvalidator.LengthAtLeast(1)
// validator (forge, b500175)
// to reject "" at plan time too. Together the two checks mean the provider
// can never again send the backend an empty name by construction, not by
// convention -- closing CR1's real-backend echo defect (see above) at the
// schema level instead of relying on client-side default computation to
// avoid it. Release impact: BREAKING, unlike A2's own PATCH-classified fix
// (shipwright report S10).
//
// TestAccCloudResourceMulti_DistinctExplicitNames still asserts CR1's
// adopt-path fix directly (2 distinct backend resources from two explicitly
// named blocks, clean re-plan). Block "a" now sets name explicitly
// ("vm-aws-us-east-2", the same value A2's client-side computation used to
// produce) since an omitted name is no longer valid HCL under Required --
// a mechanical update only, every existing assertion is unchanged.
//
// TestAccCloudResourceMulti_BothOmitName_RequiredArgumentError replaces
// TestAccCloudResourceMulti_BothOmitName_ReturnsConflict (A2: both blocks
// computed an identical name client-side and 409'd at apply time on the
// second add_resource call). Under Required-name, omitting name from either
// block is a Terraform Core configuration error caught at PLAN time --
// there is no client-side default left to compute, so the scenario can no
// longer reach the API at all. This is a strictly earlier, louder failure
// than A2's apply-time 409, proven here by asserting mock.resourceCount()
// stays at 0 (neither block's add_resource ever runs), not just that some
// error occurred.
//
// TestAccCloudResourceMulti_EmptyName_PlanError is new (architect
// 2026-07-08, closing the empty-string gap): asserts the LengthAtLeast(1)
// validator rejects name = "" at plan time, the same tier as the
// required-argument check above and for the same reason -- an empty name
// must never reach add_resource, full stop.
//
// TestAccCloudResourceMulti_DuplicateExplicitName_ReturnsConflict is
// unaffected by Required-name -- both blocks already used identical
// explicit names before this change, so neither the Required flip nor the
// LengthAtLeast(1) validator changes its behavior. Still the one test in
// this file that is fork-independent, verified green under every design
// this file has ever tracked (Option B, A2, and now Required-name).

// multiResourceMockEntry is one backend cloud_resource record tracked by the
// mock across the lifetime of a single test.
type multiResourceMockEntry struct {
	Name              string
	Provider          string
	ComputeStack      string
	Region            string
	NetworkingMode    string
	CloudResourceID   string
	CloudDeploymentID string
}

// multiResourceMockServer is a CREATE-ONLY, in-memory fake of one cloud's
// resource collection, modeled directly on the traced backend behavior
// above: add_resource never updates an existing row. A name collision is
// always rejected (409), and an omitted name is generated with the same
// {stack}-{provider}-{region}[-N] scheme the real DAO uses, so the mock can't
// accidentally pass a test via upsert semantics the backend doesn't have.
type multiResourceMockServer struct {
	t       *testing.T
	cloudID string

	mu      sync.Mutex
	order   []string // names in creation order; order[0] is_default
	byName  map[string]*multiResourceMockEntry
	nextSeq int
}

func newMultiResourceMockServer(t *testing.T, cloudID string) (*httptest.Server, *multiResourceMockServer) {
	t.Helper()
	m := &multiResourceMockServer{
		t:       t,
		cloudID: cloudID,
		byName:  make(map[string]*multiResourceMockEntry),
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/clouds", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.Method {
		case http.MethodPost:
			_, _ = fmt.Fprintf(w, `{"result": %s}`, m.cloudJSON())
		case http.MethodGet:
			// anyscale_cloud's own by-name adopt check (a separate mechanism
			// from cloud_resource's, unrelated to CR1) - report no existing
			// cloud so Create always takes the fresh-create path.
			_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds", r.Method)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"result": %s}`, m.cloudJSON())
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s on /api/v2/clouds/%s", r.Method, cloudID)
		}
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/machine_pools", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"results": [], "metadata": {"total": 0, "next_paging_token": null}}`)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/resources", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		results := make([]map[string]any, 0, len(m.order))
		for _, name := range m.order {
			results = append(results, m.renderLocked(name))
		}
		total := len(m.order)
		m.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		body, _ := json.Marshal(map[string]any{
			"results":  results,
			"metadata": map[string]any{"total": total, "next_paging_token": nil},
		})
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/add_resource", func(w http.ResponseWriter, r *http.Request) {
		var req provider.CloudDeploymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode add_resource body: %v", err)
		}

		m.mu.Lock()
		defer m.mu.Unlock()

		requestName := req.Name // preserved verbatim for the response-echo below
		name := req.Name
		if name == "" {
			name = m.generateNameLocked(req.ComputeStack, req.Provider, req.Region)
		} else if _, exists := m.byName[name]; exists {
			// Real backend: plain INSERT, DB unique index on (cloud_id, name)
			// catches the collision. Create-only - never an upsert.
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintf(w, `{"detail": "A cloud deployment with the name %s already exists in this cloud."}`, name)
			return
		}

		m.nextSeq++
		m.byName[name] = &multiResourceMockEntry{
			Name:              name,
			Provider:          req.Provider,
			ComputeStack:      req.ComputeStack,
			Region:            req.Region,
			NetworkingMode:    req.NetworkingMode,
			CloudResourceID:   fmt.Sprintf("cr_mock_%d", m.nextSeq),
			CloudDeploymentID: fmt.Sprintf("cd_mock_%d", m.nextSeq),
		}
		m.order = append(m.order, name)

		w.WriteHeader(http.StatusOK)
		// Real backend fidelity (clouds_resource.py add_cloud_resource,
		// ~L2460-2570): the HTTP response echoes the ORIGINAL request
		// object, not the DAO's persisted row -- so a request that omitted
		// name gets an empty name back even though a real one was generated
		// and persisted internally. A2 means the provider never sends an
		// empty name in normal operation, so this echo is a no-op today
		// (requestName == name whenever a caller supplies one). It exists
		// as a tripwire: if the provider ever regresses to sending empty
		// names again, this mock now reproduces the exact real "the name
		// never comes back" failure instead of the too-helpful auto-return
		// an earlier version of this file used.
		body, _ := json.Marshal(map[string]any{"result": m.renderEchoLocked(name, requestName)})
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/api/v2/clouds/"+cloudID+"/remove_resource", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("cloud_resource_name")
		m.mu.Lock()
		delete(m.byName, name)
		for i, n := range m.order {
			if n == name {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, m
}

// generateNameLocked mirrors cloud_resources_dao.py's
// _generate_cloud_resource_name: {compute_stack}-{provider}-{region},
// lowercase, suffixed with -{count} when a resource sharing that exact
// tuple already exists on the cloud. Must be called with m.mu held.
func (m *multiResourceMockServer) generateNameLocked(computeStack, provider, region string) string {
	base := strings.ToLower(fmt.Sprintf("%s-%s-%s", computeStack, provider, region))
	count := 0
	for _, name := range m.order {
		e := m.byName[name]
		if strings.EqualFold(e.ComputeStack, computeStack) && strings.EqualFold(e.Provider, provider) && strings.EqualFold(e.Region, region) {
			count++
		}
	}
	if count == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, count)
}

// renderLocked must be called with m.mu held. It reflects the persisted row
// -- what a subsequent GET/list call would see (real backend reads, unlike
// add_resource's response, are not affected by the echo-the-request bug).
func (m *multiResourceMockServer) renderLocked(name string) map[string]any {
	e := m.byName[name]
	return map[string]any{
		"cloud_resource_id":   e.CloudResourceID,
		"cloud_deployment_id": e.CloudDeploymentID,
		"name":                e.Name,
		"provider":            e.Provider,
		"compute_stack":       e.ComputeStack,
		"region":              e.Region,
		"networking_mode":     e.NetworkingMode,
		"is_default":          len(m.order) > 0 && m.order[0] == name,
	}
}

// renderEchoLocked is renderLocked with "name" overridden to exactly what
// the add_resource request sent (echoRequestName), reproducing the real
// backend's add_cloud_resource response (which returns the request object,
// not the persisted DAO row). persistedName is the entry's real, internally
// generated-or-explicit name, used to look up every other field. Must be
// called with m.mu held.
func (m *multiResourceMockServer) renderEchoLocked(persistedName, echoRequestName string) map[string]any {
	rendered := m.renderLocked(persistedName)
	rendered["name"] = echoRequestName
	return rendered
}

func (m *multiResourceMockServer) cloudJSON() string {
	return fmt.Sprintf(`{
		"id": %[1]q, "name": "multi-resource-mock", "provider": "AWS", "region": "us-east-2",
		"status": "ready", "state": "ACTIVE", "compute_stack": "VM"
	}`, m.cloudID)
}

// resourceCount returns the number of distinct backend resources currently
// tracked - the most direct proxy for "did CR1's adopt-bug collapse two
// blocks into one."
func (m *multiResourceMockServer) resourceCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.order)
}

func testAccMultiResourceCloudBlock() string {
	return `
resource "anyscale_cloud" "test" {
  name = "multi-resource-mock"
}
`
}

// TestAccCloudResourceMulti_DistinctExplicitNames is acceptance criterion 1
// (CLOUD-RESOURCE-DESIGN.md): two anyscale_cloud_resource blocks with
// distinct explicit names on one cloud must produce 2 distinct backend
// resources and a clean, empty re-plan. depends_on forces block "b" to apply
// strictly after block "a" commits - without it Terraform's default
// parallelism could race the two Creates, which wouldn't test this scenario
// deterministically either pre- or post-fix.
func TestAccCloudResourceMulti_DistinctExplicitNames(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_multi_mock_a"
	server, mock := newMultiResourceMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + testAccMultiResourceCloudBlock() + `
resource "anyscale_cloud_resource" "a" {
  cloud_id      = anyscale_cloud.test.id
  name          = "vm-aws-us-east-2"
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = "vpc-a"
    subnet_ids_to_az = {
      "subnet-a1" = "us-east-2a"
    }
    security_group_ids        = ["sg-a"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/a-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/a-cluster-node"
    external_id               = "a-external-id"
  }

  object_storage {
    bucket_name = "bucket-a"
  }
}

resource "anyscale_cloud_resource" "b" {
  cloud_id      = anyscale_cloud.test.id
  name          = "vm-aws-us-east-2-secondary"
  region        = "us-east-2"
  compute_stack = "VM"
  depends_on    = [anyscale_cloud_resource.a]

  aws_config {
    vpc_id                    = "vpc-b"
    subnet_ids_to_az = {
      "subnet-b1" = "us-east-2a"
    }
    security_group_ids        = ["sg-b"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/b-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/b-cluster-node"
    external_id               = "b-external-id"
  }

  object_storage {
    bucket_name = "bucket-b"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("anyscale_cloud_resource.a", "name", "vm-aws-us-east-2"),
					resource.TestCheckResourceAttr("anyscale_cloud_resource.b", "name", "vm-aws-us-east-2-secondary"),
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.a", "cloud_resource_id"),
					resource.TestCheckResourceAttrSet("anyscale_cloud_resource.b", "cloud_resource_id"),
					testAccCheckAttrsDiffer("anyscale_cloud_resource.a", "cloud_resource_id", "anyscale_cloud_resource.b", "cloud_resource_id"),
					func(*terraform.State) error {
						if got := mock.resourceCount(); got != 2 {
							return fmt.Errorf("expected 2 distinct backend resources after apply (acceptance criterion 1), mock has %d - the two blocks were collapsed into one (CR1 adopt-bug)", got)
						}
						return nil
					},
				),
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCloudResourceMulti_BothOmitName_RequiredArgumentError supersedes
// this test's previous assertion under both Option B (2 resources via
// backend auto-suffix) and A2 (identical client-side-computed names, 409 at
// apply time -- see git history for both). Required-name (architect
// DECISION 2026-07-08, superseding A2's client-side name computation
// entirely) leaves name with no default of any kind, so omitting it on
// either block is now a Terraform Core configuration error caught at PLAN
// time, before the provider is ever invoked to diff or apply -- there is no
// client-side computation left to produce a colliding name in the first
// place. Both blocks omit name here specifically to prove the check is
// per-attribute and unconditional, not somehow satisfied by one block's
// presence covering the other. Additional resources on the same
// cloud/region/stack/provider still need an EXPLICIT distinct name (see
// DistinctExplicitNames above) -- documented behavior, not a bug.
func TestAccCloudResourceMulti_BothOmitName_RequiredArgumentError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_multi_mock_b"
	server, mock := newMultiResourceMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + testAccMultiResourceCloudBlock() + `
resource "anyscale_cloud_resource" "a" {
  cloud_id      = anyscale_cloud.test.id
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = "vpc-a"
    subnet_ids_to_az = {
      "subnet-a1" = "us-east-2a"
    }
    security_group_ids        = ["sg-a"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/a-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/a-cluster-node"
    external_id               = "a-external-id"
  }

  object_storage {
    bucket_name = "bucket-a"
  }
}

resource "anyscale_cloud_resource" "b" {
  cloud_id      = anyscale_cloud.test.id
  region        = "us-east-2"
  compute_stack = "VM"
  depends_on    = [anyscale_cloud_resource.a]

  aws_config {
    vpc_id                    = "vpc-b"
    subnet_ids_to_az = {
      "subnet-b1" = "us-east-2a"
    }
    security_group_ids        = ["sg-b"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/b-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/b-cluster-node"
    external_id               = "b-external-id"
  }

  object_storage {
    bucket_name = "bucket-b"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?i)missing required argument|argument "name" is required`),
			},
		},
	})

	// Neither block's add_resource may ever run -- Terraform Core rejects
	// the whole configuration before the provider is invoked for planning,
	// let alone applying. This is the concrete proof that Required-name
	// closes the omitted-name path strictly earlier than A2's apply-time
	// 409 did.
	if got := mock.resourceCount(); got != 0 {
		t.Errorf("expected 0 backend resources -- a required-argument config error must block both blocks before any add_resource call, mock has %d", got)
	}
}

// TestAccCloudResourceMulti_EmptyName_PlanError asserts the second half of
// architect's empty-name closure (2026-07-08): Required:true alone stops an
// OMITTED name (see BothOmitName_RequiredArgumentError above) but does not
// stop an explicitly EMPTY one -- name = "" is a valid "present" value under
// plain Required. The schema's stringvalidator.LengthAtLeast(1)
// (resource_cloud_resource.go)
// closes that second door: an empty name must fail at plan time too, before
// any add_resource call, so the provider can never again send the backend
// an empty name -- the exact precondition of CR1's original real-backend
// defect (add_cloud_resource echoes the request's name verbatim, so an
// empty request name comes back empty even though the DAO generated a real
// one internally, see file header above). A single block is sufficient --
// this is a per-attribute value check, independent of the two-block
// collision scenarios elsewhere in this file.
func TestAccCloudResourceMulti_EmptyName_PlanError(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_multi_mock_d"
	server, mock := newMultiResourceMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + testAccMultiResourceCloudBlock() + `
resource "anyscale_cloud_resource" "a" {
  cloud_id      = anyscale_cloud.test.id
  name          = ""
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = "vpc-a"
    subnet_ids_to_az = {
      "subnet-a1" = "us-east-2a"
    }
    security_group_ids        = ["sg-a"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/a-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/a-cluster-node"
    external_id               = "a-external-id"
  }

  object_storage {
    bucket_name = "bucket-a"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?i)string length must be at least 1|invalid attribute value length`),
			},
		},
	})

	if got := mock.resourceCount(); got != 0 {
		t.Errorf("expected 0 backend resources -- an empty name must be rejected at plan time, before any add_resource call, mock has %d", got)
	}
}

// TestAccCloudResourceMulti_DuplicateExplicitName_ReturnsConflict is
// acceptance criterion 4's reframed assertion (b): a duplicate EXPLICIT name
// on the same cloud must fail loudly at apply time (mirroring the backend's
// real 409), never silently collapse. Unlike the two tests above, this one
// is fork-independent and passes both before and after CR1's fix: pre-fix,
// the adopt path overwrites block "b"'s name to match block "a"'s -- which
// happens to already equal "b"'s own explicit name here, so the add_resource
// call still collides and still 409s. Post-fix, block "b" sends its own
// explicit name unmodified and collides directly. Same observable outcome
// either way, because a real duplicate name was never survivable on real
// infra in the first place - only the mock's old (incorrect) upsert model
// ever made it look like a self-heal.
func TestAccCloudResourceMulti_DuplicateExplicitName_ReturnsConflict(t *testing.T) {
	SkipIfNotAcceptanceTest(t)

	const cloudID = "cld_multi_mock_c"
	server, _ := newMultiResourceMockServer(t, cloudID)

	config := testAccProviderBlock(server.URL) + testAccMultiResourceCloudBlock() + `
resource "anyscale_cloud_resource" "a" {
  cloud_id      = anyscale_cloud.test.id
  name          = "shared-name"
  region        = "us-east-2"
  compute_stack = "VM"

  aws_config {
    vpc_id                    = "vpc-a"
    subnet_ids_to_az = {
      "subnet-a1" = "us-east-2a"
    }
    security_group_ids        = ["sg-a"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/a-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/a-cluster-node"
    external_id               = "a-external-id"
  }

  object_storage {
    bucket_name = "bucket-a"
  }
}

resource "anyscale_cloud_resource" "b" {
  cloud_id      = anyscale_cloud.test.id
  name          = "shared-name"
  region        = "us-east-2"
  compute_stack = "VM"
  depends_on    = [anyscale_cloud_resource.a]

  aws_config {
    vpc_id                    = "vpc-b"
    subnet_ids_to_az = {
      "subnet-b1" = "us-east-2a"
    }
    security_group_ids        = ["sg-b"]
    controlplane_iam_role_arn = "arn:aws:iam::123456789012:role/b-crossaccount"
    dataplane_iam_role_arn    = "arn:aws:iam::123456789012:role/b-cluster-node"
    external_id               = "b-external-id"
  }

  object_storage {
    bucket_name = "bucket-b"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config,
				ExpectError: regexp.MustCompile(`(?i)status 409|already exists`),
			},
		},
	})
}

// testAccCheckAttrsDiffer fails unless both attributes are set and their
// values differ - the strongest available proof that two Terraform resource
// addresses backed two genuinely distinct backend objects, not just that
// apply didn't error.
func testAccCheckAttrsDiffer(addr1, attr1, addr2, attr2 string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs1, ok := s.RootModule().Resources[addr1]
		if !ok {
			return fmt.Errorf("resource not found: %s", addr1)
		}
		rs2, ok := s.RootModule().Resources[addr2]
		if !ok {
			return fmt.Errorf("resource not found: %s", addr2)
		}
		v1, v2 := rs1.Primary.Attributes[attr1], rs2.Primary.Attributes[attr2]
		if v1 == "" || v2 == "" {
			return fmt.Errorf("expected both %s.%s (%q) and %s.%s (%q) to be set", addr1, attr1, v1, addr2, attr2, v2)
		}
		if v1 == v2 {
			return fmt.Errorf("expected %s.%s and %s.%s to differ, both were %q - resources were merged (CR1 adopt-bug)", addr1, attr1, addr2, attr2, v1)
		}
		return nil
	}
}
