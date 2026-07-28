package provider

// Regression coverage for the batch_create fix: a collaborator who already has
// permissions for a project (the auto-added creator-owner is the common real
// case) must have their DECLARED permission_level actually applied via an
// authoritative follow-up write, and one such conflict must never block
// creation of OTHER, genuinely-new collaborators declared in the same apply.
// Both bugs were confirmed via real resource.Test runs before the fix - see
// docs/decisions/rbac-surface-consolidation (or the quest history) for the
// backend trace this was verified against.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// projectConflictMockServer is a small stateful mock backing a single project
// and its collaborators, shared by the three tests below - each test starts
// it with a different set of pre-existing collaborators to exercise a
// different real-world starting state. creatorEmail, if non-empty, is added
// as an owner-level collaborator as a genuine SIDE EFFECT of the
// POST /api/v2/projects call handled below - not pre-seeded before the
// server starts - so that a test's "this person already has permissions"
// precondition is actually reached the same way the real backend reaches it
// (auto-adding the creator atomically within project creation), not shortcut
// past it. This matters for the create-path tests below: the real bug this
// covers is only reproduced if the conflicting collaborator's pre-existing
// status arrives through the same request sequence a real apply would drive,
// not through state assembled directly by the test.
func projectConflictMockServer(t *testing.T, projectID, creatorEmail string) *httptest.Server {
	t.Helper()

	var mu sync.Mutex
	project := ProjectResult{
		ID:            projectID,
		Name:          "conflict-spike",
		DirectoryName: "conflict-dir",
		CreatedAt:     "2020-01-01T00:00:00Z",
	}
	var collaborators []ProjectCollaboratorResult

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		collabPrefix := fmt.Sprintf("/api/v2/projects/%s/collaborators/", projectID)

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects":
			var body CreateProjectRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			project.Name = body.Name
			project.Description = body.Description
			project.ParentCloudID = &body.ParentCloudID
			// Real side effect of real project creation, matching the actual
			// backend: the creator is auto-added as owner atomically within
			// this same call, before this handler ever returns - so by the
			// time any later request arrives, they are already a genuine
			// collaborator, exactly as in production.
			if creatorEmail != "" && len(collaborators) == 0 {
				creator := ProjectCollaboratorResult{ID: "ident_creator", PermissionLevel: "owner"}
				creator.Value.ID = "usr_creator"
				creator.Value.Email = creatorEmail
				collaborators = append(collaborators, creator)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: project})

		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v2/projects/%s", projectID):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: project})

		case r.Method == http.MethodPost && r.URL.Path == collabPrefix+"users/batch_create":
			var entries ProjectCollaboratorBatchRequest
			_ = json.NewDecoder(r.Body).Decode(&entries)

			// Faithful reproduction of the real backend's
			// batch_create_project_collaborators: validate every entry
			// first - if ANY email already has permissions, 409 the WHOLE
			// request with the real detail text and write NOTHING.
			for _, e := range entries {
				for _, existing := range collaborators {
					if strings.EqualFold(existing.Value.Email, e.Value.Email) {
						w.WriteHeader(http.StatusConflict)
						_, _ = fmt.Fprintf(w,
							`{"error":{"detail":"User with email %s already has permissions for this project."}}`,
							e.Value.Email,
						)
						return
					}
				}
			}
			for _, e := range entries {
				n := len(collaborators) + 1
				res := ProjectCollaboratorResult{ID: fmt.Sprintf("ident_%d", n), PermissionLevel: e.PermissionLevel}
				res.Value.ID = fmt.Sprintf("usr_%d", n)
				res.Value.Email = e.Value.Email
				collaborators = append(collaborators, res)
			}
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == collabPrefix+"users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{Results: collaborators})

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, collabPrefix):
			identityID := strings.TrimPrefix(r.URL.Path, collabPrefix)
			var update ProjectCollaboratorUpdateRequest
			_ = json.NewDecoder(r.Body).Decode(&update)
			for i := range collaborators {
				if collaborators[i].ID == identityID {
					collaborators[i].PermissionLevel = update.PermissionLevel
				}
			}
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, collabPrefix):
			identityID := strings.TrimPrefix(r.URL.Path, collabPrefix)
			for i, c := range collaborators {
				if c.ID == identityID {
					collaborators = append(collaborators[:i], collaborators[i+1:]...)
					break
				}
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && r.URL.Path == fmt.Sprintf("/api/v2/projects/%s", projectID):
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Logf("PROJECT CONFLICT MOCK: unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func projectConflictProtoV6Factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"anyscale": providerserver.NewProtocol6WithError(NewFramework("test")()),
	}
}

// TestProjectResourceCollaboratorConflict_ValueChangeAppliesOnCreate proves
// case 3(a): declaring an existing collaborator (the auto-added creator-owner)
// at a DIFFERENT permission_level than they currently hold must actually
// apply that change via the authoritative follow-up write, not silently
// no-op it. Exercises the Create path.
func TestProjectResourceCollaboratorConflict_ValueChangeAppliesOnCreate(t *testing.T) {
	const projectID = "prj_conflict_create_valuechange"
	const creatorEmail = "creator@example.com"

	server := projectConflictMockServer(t, projectID, creatorEmail)

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-conflict-create-valuechange")

	const config = `
resource "anyscale_project" "test" {
  name        = "conflict-spike"
  cloud_id    = "cld_spike"
  description = "conflict spike"

  collaborator {
    email            = "creator@example.com"
    permission_level = "readonly"
  }
}
`

	tfresource.UnitTest(t, tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   config,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "readonly"),
				),
			},
		},
	})
}

// TestProjectResourceCollaboratorConflict_ValueChangeAppliesOnUpdate is case
// 3(a)'s Update-path counterpart (the Create/Update asymmetry found during
// design review means these need separate coverage, not just Create's):
// state starts with NO collaborators tracked (matching the Read-gating
// behavior where a project managed without a collaborator block never picks
// up the real creator), then config adds the creator at a permission_level
// that differs from their real current one.
func TestProjectResourceCollaboratorConflict_ValueChangeAppliesOnUpdate(t *testing.T) {
	const projectID = "prj_conflict_update_valuechange"
	const creatorEmail = "creator@example.com"

	server := projectConflictMockServer(t, projectID, creatorEmail)

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-conflict-update-valuechange")

	const configNoCollaborators = `
resource "anyscale_project" "test" {
  name        = "conflict-spike"
  cloud_id    = "cld_spike"
  description = "conflict spike"
}
`
	const configWithCreatorReadonly = `
resource "anyscale_project" "test" {
  name        = "conflict-spike"
  cloud_id    = "cld_spike"
  description = "conflict spike"

  collaborator {
    email            = "creator@example.com"
    permission_level = "readonly"
  }
}
`

	tfresource.UnitTest(t, tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   configNoCollaborators,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "0"),
				),
			},
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   configWithCreatorReadonly,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.permission_level", "readonly"),
				),
			},
		},
	})
}

// TestProjectResourceCollaboratorConflict_WholeBatchNoLongerBlocksInnocentCollaborators
// proves case 3(b): declaring a pre-existing collaborator (the creator)
// alongside genuinely new ones in the same apply must create ALL of them -
// the pre-existing one's conflict must never collateral-damage the others.
// This is the same scenario as the throwaway pre-fix spike
// (spike_batch_create_whole_batch_409_test.go), now as a permanent,
// real-assertion regression test proving the FIX rather than just the bug.
func TestProjectResourceCollaboratorConflict_WholeBatchNoLongerBlocksInnocentCollaborators(t *testing.T) {
	const projectID = "prj_conflict_wholebatch"
	const creatorEmail = "creator@example.com"

	server := projectConflictMockServer(t, projectID, creatorEmail)

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-conflict-wholebatch")

	const config = `
resource "anyscale_project" "test" {
  name        = "conflict-spike"
  cloud_id    = "cld_spike"
  description = "conflict spike"

  collaborator {
    email            = "creator@example.com"
    permission_level = "owner"
  }
  collaborator {
    email            = "new1@example.com"
    permission_level = "write"
  }
  collaborator {
    email            = "new2@example.com"
    permission_level = "owner"
  }
}
`

	tfresource.UnitTest(t, tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   config,
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "3"),
				),
			},
		},
	})
}

// TestProjectResourceCollaboratorConflict_UpdatePartialFailureStillSyncsState
// is a design-review finding (Create/Update state-sync asymmetry): Update
// used to bare-return on a syncCollaborators error without ever calling
// resp.State.Set, and Update's state is pre-seeded from PriorState by the
// framework - so a partial fail-fast stop (one collaborator created
// successfully, the next one in the same apply fails for a real reason) used
// to leave state reverted to stale pre-apply values instead of reflecting
// what was actually applied. This proves the fix: after a partial failure,
// state reflects the real backend (the one collaborator that did get
// created), not the old empty list.
func TestProjectResourceCollaboratorConflict_UpdatePartialFailureStillSyncsState(t *testing.T) {
	const projectID = "prj_conflict_update_partial"

	var mu sync.Mutex
	project := ProjectResult{
		ID:            projectID,
		Name:          "conflict-partial",
		DirectoryName: "conflict-dir",
		CreatedAt:     "2020-01-01T00:00:00Z",
	}
	var collaborators []ProjectCollaboratorResult

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		collabPrefix := fmt.Sprintf("/api/v2/projects/%s/collaborators/", projectID)

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/projects":
			var body CreateProjectRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			project.Name = body.Name
			project.Description = body.Description
			project.ParentCloudID = &body.ParentCloudID
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: project})

		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v2/projects/%s", projectID):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectResponse{Result: project})

		case r.Method == http.MethodPost && r.URL.Path == collabPrefix+"users/batch_create":
			var entries ProjectCollaboratorBatchRequest
			_ = json.NewDecoder(r.Body).Decode(&entries)
			// Exactly one entry per call after the fix (never batched).
			if len(entries) == 1 && entries[0].Value.Email == "fail@example.com" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"detail":"a real, non-conflict backend failure"}}`))
				return
			}
			for _, e := range entries {
				n := len(collaborators) + 1
				res := ProjectCollaboratorResult{ID: fmt.Sprintf("ident_%d", n), PermissionLevel: e.PermissionLevel}
				res.Value.ID = fmt.Sprintf("usr_%d", n)
				res.Value.Email = e.Value.Email
				collaborators = append(collaborators, res)
			}
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == collabPrefix+"users":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ProjectCollaboratorListResponse{Results: collaborators})

		case r.Method == http.MethodDelete && r.URL.Path == fmt.Sprintf("/api/v2/projects/%s", projectID):
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Logf("PARTIAL FAILURE MOCK: unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	t.Setenv("ANYSCALE_API_URL", server.URL)
	t.Setenv("ANYSCALE_CLI_TOKEN", "fake-token-conflict-update-partial")

	const configNoCollaborators = `
resource "anyscale_project" "test" {
  name        = "conflict-partial"
  cloud_id    = "cld_spike"
  description = "conflict partial"
}
`
	// ok@example.com must come first in config so syncCollaborators (iterating
	// planned in order, per the deterministic-order fix) attempts it before
	// fail@example.com - proving state reflects the one that DID succeed.
	const configOneOkOneFails = `
resource "anyscale_project" "test" {
  name        = "conflict-partial"
  cloud_id    = "cld_spike"
  description = "conflict partial"

  collaborator {
    email            = "ok@example.com"
    permission_level = "write"
  }
  collaborator {
    email            = "fail@example.com"
    permission_level = "owner"
  }
}
`

	tfresource.UnitTest(t, tfresource.TestCase{
		Steps: []tfresource.TestStep{
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   configNoCollaborators,
			},
			{
				ProtoV6ProviderFactories: projectConflictProtoV6Factories(),
				Config:                   configOneOkOneFails,
				ExpectError:              regexp.MustCompile("a real, non-conflict backend failure"),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.#", "1"),
					tfresource.TestCheckResourceAttr("anyscale_project.test", "collaborator.0.email", "ok@example.com"),
				),
			},
		},
	})
}
