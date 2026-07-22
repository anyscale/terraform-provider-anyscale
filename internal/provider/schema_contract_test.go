package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// These descriptions are hardcoded, stable strings on the framework's own
// modifier types (see stringplanmodifier.RequiresReplace /
// UseStateForUnknown) and are the only exported way to identify which
// modifier is present without depending on their unexported concrete types.
const (
	descRequiresReplace    = "If the value of this attribute changes, Terraform will destroy and recreate the resource."
	descUseStateForUnknown = "Once set, the value of this attribute in state will not change."
	// descRequiresReplaceIfConfigured is distinct from descRequiresReplace
	// (note "is configured and") — that difference is exactly what lets a
	// test tell the two modifiers apart, since neither exposes its
	// underlying type publicly.
	descRequiresReplaceIfConfigured = "If the value of this attribute is configured and changes, Terraform will destroy and recreate the resource."
	// descUseNonNullStateForUnknown is distinct from descUseStateForUnknown
	// (note "to a non-null value") — required instead of plain
	// UseStateForUnknown for an attribute nested inside a list, because
	// UseStateForUnknown copies a MISSING element's null state into the plan
	// for an update that adds a brand-new list element, producing "Provider
	// produced inconsistent result after apply" (task 1f2d592f, found via a
	// live update-add-worker-group repro).
	descUseNonNullStateForUnknown = "Once set to a non-null value, the value of this attribute in state will not change."
	// descRegionSemanticEqual matches regionSemanticEqualPlanModifier's own
	// Description() (cloud_helpers.go) - our own unexported type, unlike the
	// framework built-ins above, but the same identify-by-Description
	// technique applies since PlanModifiers is a slice of the planmodifier.String
	// interface either way.
	descRegionSemanticEqual = "Treats object_storage.region as unchanged when it equals the resource's own region and the prior state has no region recorded, since the backend cannot distinguish an explicitly-matching region from one that was never set."
)

// schemaOf returns the resource.Schema for a resource.Resource implementation
// by calling Schema() directly, with no provider server or API client needed.
func schemaOf(t *testing.T, r resource.Resource) schema.Schema {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema() returned diagnostics: %s", resp.Diagnostics)
	}
	return resp.Schema
}

// datasourceSchemaOf is schemaOf's data-source analogue. datasource.Schema
// and resource/schema.Schema are distinct Go types even where structurally
// identical, so they need separate helpers.
func datasourceSchemaOf(t *testing.T, d datasource.DataSource) dsschema.Schema {
	t.Helper()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema() returned diagnostics: %s", resp.Diagnostics)
	}
	return resp.Schema
}

func hasPlanModifierDescription(mods []planmodifier.String, want string) bool {
	for _, m := range mods {
		if m.Description(context.Background()) == want {
			return true
		}
	}
	return false
}

// indexOfPlanModifierDescription returns the index of the first modifier in
// mods whose Description matches want, or -1 if absent. Order matters for
// UseStateForUnknown vs. RequiresReplace specifically: the framework runs
// PlanModifiers sequentially, feeding each one's output into the next one's
// input (internal/fwserver/attribute_plan_modification.go). UseStateForUnknown
// declared AFTER RequiresReplace runs RequiresReplace while the planned value
// is still Unknown (an omitted Computed field's framework default) - Unknown
// never equals the known state value, so RequiresReplace fires on every
// plan, silently reproducing the exact replace-on-import bug this ordering
// exists to fix. Presence-only checks (hasPlanModifierDescription) cannot
// catch a backwards order - both modifiers would still be "present".
func indexOfPlanModifierDescription(mods []planmodifier.String, want string) int {
	for i, m := range mods {
		if m.Description(context.Background()) == want {
			return i
		}
	}
	return -1
}

func hasMapPlanModifierDescription(mods []planmodifier.Map, want string) bool {
	for _, m := range mods {
		if m.Description(context.Background()) == want {
			return true
		}
	}
	return false
}

// indexOfListPlanModifierDescription is indexOfPlanModifierDescription's
// planmodifier.List analogue - same ordering hazard, same reasoning, for
// List-typed attributes like file_storage.mount_targets.
func indexOfListPlanModifierDescription(mods []planmodifier.List, want string) int {
	for i, m := range mods {
		if m.Description(context.Background()) == want {
			return i
		}
	}
	return -1
}

func hasInt64PlanModifierDescription(mods []planmodifier.Int64, want string) bool {
	for _, m := range mods {
		if m.Description(context.Background()) == want {
			return true
		}
	}
	return false
}

// hasInt64ValidatorContaining reports whether any validator's Description
// contains want. Int64 validators do not expose their bound as a typed
// field, only as a human-readable Description string (e.g.
// int64validator.AtLeast(0).Description() = "value must be at least 0"), so
// substring matching is the only externally-observable way to pin a specific
// bound without depending on the validator's unexported concrete type.
func hasInt64ValidatorContaining(vs []validator.Int64, want string) bool {
	for _, v := range vs {
		if strings.Contains(v.Description(context.Background()), want) {
			return true
		}
	}
	return false
}

// TestServerInferredStringAttributesAreComputedWithUseStateForUnknown pins
// the schema contract for "server-inferred creation-time" string attributes:
// ones the user may omit (the server derives/defaults a value, e.g.
// compute_stack defaulting to VM) or set explicitly, where the value is fixed
// for the life of the resource (hence RequiresReplace).
//
// Omit-or-set-explicitly requires ALL THREE of Optional, Computed, and
// UseStateForUnknown together. Optional without Computed lets Terraform plan
// an omitted config value as a hard null; when the API then returns a
// non-null value after apply, the framework has no way to reconcile the two
// and errors with "Provider produced inconsistent result after apply".
// UseStateForUnknown is what keeps that resolved value stable across later
// plans and import round-trips instead of showing a perpetual diff.
//
// anyscale_cloud.compute_stack shipped without Computed (see
// .crystl/quest/spec.json finding F1) while its siblings cloud_provider and
// region on the same resource had the full set — a schema copy/paste gap
// that only a live acceptance-test apply could catch, because nothing
// exercises plan-consistency at the schema level. This test catches the same
// class of regression in milliseconds instead of a ~30s+ acceptance test run.
func TestServerInferredStringAttributesAreComputedWithUseStateForUnknown(t *testing.T) {
	cases := []struct {
		resource  resource.Resource
		attribute string
	}{
		{&CloudResource{}, "compute_stack"},
		{&CloudResource{}, "cloud_provider"},
		{&CloudResource{}, "region"},
		{&CloudResourceResource{}, "compute_stack"},
		{&CloudResourceResource{}, "cloud_provider"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%T/%s", tc.resource, tc.attribute), func(t *testing.T) {
			s := schemaOf(t, tc.resource)
			attr, ok := s.Attributes[tc.attribute]
			if !ok {
				t.Fatalf("attribute %q not found in schema", tc.attribute)
			}
			strAttr, ok := attr.(schema.StringAttribute)
			if !ok {
				t.Fatalf("attribute %q is not a schema.StringAttribute (got %T)", tc.attribute, attr)
			}

			if !strAttr.Optional {
				t.Errorf("%q must be Optional: true (server-inferred attributes may be omitted by the user)", tc.attribute)
			}
			if !strAttr.Computed {
				t.Errorf("%q must be Computed: true — without it, omitting the attribute plans a hard null that can never "+
					"reconcile against a non-null API response, producing 'Provider produced inconsistent result after apply'", tc.attribute)
			}
			if !hasPlanModifierDescription(strAttr.PlanModifiers, descUseStateForUnknown) {
				t.Errorf("%q must include stringplanmodifier.UseStateForUnknown() so the resolved value is stable "+
					"across subsequent plans and import round-trips", tc.attribute)
			}
			if !hasPlanModifierDescription(strAttr.PlanModifiers, descRequiresReplace) {
				t.Errorf("%q must include stringplanmodifier.RequiresReplace() — it is a creation-time property", tc.attribute)
			}
		})
	}
}

// TestComputeConfigResourceContract pins the schema contract settled by F11
// (the compute-config "Provider returned invalid result object after apply"
// bug, surfaced once the pinned static cloud let the compute-config tests run):
//
//   - cloud_resource must be Optional and NOT Computed. The API does not echo
//     this field back, so marking it Computed makes it unsatisfiable — Create
//     cannot resolve it and the framework rejects the apply with an unknown
//     value. Re-adding Computed here is exactly the regression this pins. This
//     is the genuinely load-bearing assertion (it holds independent of F11's
//     runtime fix).
//   - head_node.resources and worker_nodes[].resources must be
//     Optional+Computed+UseStateForUnknown. The API DOES echo these (auto-filled
//     from instance_type), so Computed is correct; this documents that intent
//     and keeps the framework's known-after-apply enforcement live.
//
// NOTE: this is a SCHEMA-contract guard — it does NOT catch F11's RUNTIME bug
// (Create leaving the Computed resources maps unknown). That is covered by the
// compute-config acceptance tests running against the pinned static cloud. The
// value here is preventing a regression of the schema DIRECTION.
func TestComputeConfigResourceContract(t *testing.T) {
	s := schemaOf(t, &ComputeConfigResource{})

	// cloud_resource: Optional and NOT Computed (the API does not echo it).
	cr, ok := s.Attributes["cloud_resource"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("cloud_resource is not a schema.StringAttribute (got %T)", s.Attributes["cloud_resource"])
	}
	if !cr.Optional {
		t.Errorf("cloud_resource must be Optional: true")
	}
	if cr.Computed {
		t.Errorf("cloud_resource must NOT be Computed — the API does not echo it back, so Computed is unsatisfiable: " +
			"Create leaves it unknown and the framework rejects the apply ('Provider returned invalid result object after apply'). " +
			"This is F11's regression guard.")
	}

	// head_node.resources and worker_nodes[].resources: Optional+Computed+USFU.
	assertResourcesMap := func(t *testing.T, label string, attrs map[string]schema.Attribute) {
		t.Helper()
		ra, ok := attrs["resources"].(schema.MapAttribute)
		if !ok {
			t.Fatalf("%s.resources is not a schema.MapAttribute (got %T)", label, attrs["resources"])
		}
		if !ra.Optional {
			t.Errorf("%s.resources must be Optional: true", label)
		}
		if !ra.Computed {
			t.Errorf("%s.resources must be Computed: true (the API auto-fills it from instance_type)", label)
		}
		if !hasMapPlanModifierDescription(ra.PlanModifiers, descUseStateForUnknown) {
			t.Errorf("%s.resources must include mapplanmodifier.UseStateForUnknown()", label)
		}
	}

	headNode, ok := s.Attributes["head_node"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("head_node is not a schema.SingleNestedAttribute (got %T)", s.Attributes["head_node"])
	}
	assertResourcesMap(t, "head_node", headNode.Attributes)

	workerNodes, ok := s.Attributes["worker_nodes"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("worker_nodes is not a schema.ListNestedAttribute (got %T)", s.Attributes["worker_nodes"])
	}
	assertResourcesMap(t, "worker_nodes[]", workerNodes.NestedObject.Attributes)
}

// TestComputeConfigCC1RequiredResourcesRename pins CC1: physical_resources
// was renamed to required_resources on both head_node and worker_nodes
// because the Anyscale API rejects physical_resources outright on any
// non-empty value (verified against the Platform backend - a non-empty
// physical_resources dict raises a ValueError). The ABSENCE assertion is the
// one that actually catches a regression: it is the only thing that would
// fail if a future refactor accidentally reintroduced the old attribute name
// (e.g. a bad merge, or copying a stale code snippet), which schema.Version
// alone would not catch. cpu_architecture (CC4) ships as a plain string with
// no enum validator - tightening it later would itself be a breaking change,
// per the ratified contract, so its absence is pinned here too.
func TestComputeConfigCC1RequiredResourcesRename(t *testing.T) {
	s := schemaOf(t, &ComputeConfigResource{})

	assertRequiredResources := func(t *testing.T, label string, attrs map[string]schema.Attribute) {
		t.Helper()

		if _, present := attrs["physical_resources"]; present {
			t.Errorf("%s must NOT have a physical_resources attribute — the backend rejects it outright (CC1); "+
				"this is the regression guard for an accidental revert of the rename", label)
		}

		rr, ok := attrs["required_resources"].(schema.SingleNestedAttribute)
		if !ok {
			t.Fatalf("%s.required_resources is not a schema.SingleNestedAttribute (got %T)", label, attrs["required_resources"])
		}
		if !rr.Optional {
			t.Errorf("%s.required_resources must be Optional: true", label)
		}

		wantFields := []string{"cpu", "memory", "gpu", "accelerator", "tpu", "tpu_hosts", "cpu_architecture"}
		for _, field := range wantFields {
			if _, ok := rr.Attributes[field]; !ok {
				t.Errorf("%s.required_resources is missing field %q", label, field)
			}
		}

		cpuArch, ok := rr.Attributes["cpu_architecture"].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s.required_resources.cpu_architecture is not a schema.StringAttribute (got %T)", label, rr.Attributes["cpu_architecture"])
		}
		if !cpuArch.Optional {
			t.Errorf("%s.required_resources.cpu_architecture must be Optional: true", label)
		}
		if len(cpuArch.Validators) > 0 {
			t.Errorf("%s.required_resources.cpu_architecture must NOT have validators — it ships as a permissive "+
				"plain string with no client-side enum by deliberate choice (CC4): the backend does not enforce one, "+
				"and tightening a validator later, after users have set values, would itself be a breaking change", label)
		}
	}

	headNode, ok := s.Attributes["head_node"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("head_node is not a schema.SingleNestedAttribute (got %T)", s.Attributes["head_node"])
	}
	assertRequiredResources(t, "head_node", headNode.Attributes)

	workerNodes, ok := s.Attributes["worker_nodes"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("worker_nodes is not a schema.ListNestedAttribute (got %T)", s.Attributes["worker_nodes"])
	}
	assertRequiredResources(t, "worker_nodes[]", workerNodes.NestedObject.Attributes)

	// CC1's state upgrader depends on the schema version actually being
	// bumped - UpgradeState is never invoked for a version-0-to-version-0
	// no-op, so a prior state with the old physical_resources attribute
	// would fail to decode against the new schema with no migration path.
	if s.Version != 1 {
		t.Errorf("schema Version = %d, want 1 (CC1's mandatory state upgrader depends on the version bump "+
			"actually happening — UpgradeState never runs if the version does not change)", s.Version)
	}
}

// TestComputeConfigCC2IdleAndMaxUptimeSettable pins CC2: idle_termination_minutes
// and maximum_uptime_minutes become settable on the resource (previously wired
// into the internal request/response struct but exposed nowhere on the
// resource model - only the data source could read them, and only read-only).
//
// Neither attribute has a static Default, which is a deliberate reversal of
// this contract's own first draft: idle_termination_minutes initially shipped
// with Default(120) to mirror the backend's create default, but that would
// silently force an EXISTING config's real value (e.g. imported, or set
// before this attribute existed) back to 120 on the next apply whenever the
// user's config omits it - the same silent-overwrite class CC12 fixes for
// flags. Both fields instead use UseStateForUnknown plus populating from the
// API response in Create/Update (mirroring Read), which reflects whatever
// the backend actually set once and then holds steady - see
// TestAccComputeConfigResource_Lifecycle_MockServer's empty-plan-after-refresh step
// for the acceptance-level proof that this actually holds (a schema-only
// check like this one cannot catch a RUNTIME failure to populate the value).
func TestComputeConfigCC2IdleAndMaxUptimeSettable(t *testing.T) {
	s := schemaOf(t, &ComputeConfigResource{})

	assertServerDefaultedInt64 := func(t *testing.T, name string, attr schema.Int64Attribute, wantValidatorContains string) {
		t.Helper()
		if !attr.Optional {
			t.Errorf("%s must be Optional: true", name)
		}
		if !attr.Computed {
			t.Errorf("%s must be Computed: true", name)
		}
		if attr.Default != nil {
			t.Errorf("%s must NOT have a static Default — the backend value can already differ from any "+
				"hardcoded default (e.g. imported state, or a value set before this attribute existed), and a "+
				"static Default would silently overwrite it the next time the user's config omits the attribute", name)
		}
		if !hasInt64PlanModifierDescription(attr.PlanModifiers, descUseStateForUnknown) {
			t.Errorf("%s must include int64planmodifier.UseStateForUnknown() so a server-populated value "+
				"stays stable across subsequent plans instead of re-planning Unknown on every apply "+
				"(which would silently create a brand-new compute config VERSION each time, since Update "+
				"always posts new_version:true - version inflation, not just a cosmetic diff)", name)
		}
		if !hasInt64ValidatorContaining(attr.Validators, wantValidatorContains) {
			t.Errorf("%s must have a validator whose Description contains %q", name, wantValidatorContains)
		}
	}

	idle, ok := s.Attributes["idle_termination_minutes"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("idle_termination_minutes is not a schema.Int64Attribute (got %T)", s.Attributes["idle_termination_minutes"])
	}
	assertServerDefaultedInt64(t, "idle_termination_minutes", idle, "at least 0")

	maxUptime, ok := s.Attributes["maximum_uptime_minutes"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("maximum_uptime_minutes is not a schema.Int64Attribute (got %T)", s.Attributes["maximum_uptime_minutes"])
	}
	assertServerDefaultedInt64(t, "maximum_uptime_minutes", maxUptime, "at least 1")
}

// TestComputeConfigCC3aNameRequiresReplace pins CC3a: name gets RequiresReplace.
// This is deliberately the OPPOSITE call from the Cloud effort's C11 (there
// RequiresReplace was the trap, since it would destroy heavyweight real
// infrastructure on a passive mismatch) - here the resource is a lightweight
// versioned template, and a live-verified bug (renaming silently orphaned the
// old config in the backend with no error, see the rename-orphan regression
// acceptance test) makes replace the semantically correct answer to an
// explicit rename instead.
func TestComputeConfigCC3aNameRequiresReplace(t *testing.T) {
	s := schemaOf(t, &ComputeConfigResource{})

	name, ok := s.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("name is not a schema.StringAttribute (got %T)", s.Attributes["name"])
	}
	if !hasPlanModifierDescription(name.PlanModifiers, descRequiresReplace) {
		t.Errorf("name must include stringplanmodifier.RequiresReplace() (CC3a) — without it, renaming a compute " +
			"config silently creates an orphaned, unmanaged duplicate in the backend instead of erroring or " +
			"replacing (live-verified bug that motivated this fix)")
	}
}

// TestComputeConfigCC6DataSourceTopologyParity pins CC6: the data source
// gains zones/head_node/worker_nodes, matching the resource's node topology.
// All three must be Computed-only (a data source has no Optional/Required
// distinction) and, critically, NOT masked the way the resource's
// maskNodeFromPrior masks Computed sub-attributes back to null — masking
// exists only to protect a RESOURCE plan from drift against a prior state
// the data source has no equivalent of, so a data source should report
// exactly what the API returns. This test only pins the schema shape
// (Computed-only); the "actually unmasked at runtime" half is an acceptance
// concern covered by TestAccComputeConfigDataSource_Basic asserting real
// head_node.instance_type values on real AWS.
func TestComputeConfigCC6DataSourceTopologyParity(t *testing.T) {
	s := datasourceSchemaOf(t, &ComputeConfigDataSource{})

	zones, ok := s.Attributes["zones"].(dsschema.ListAttribute)
	if !ok {
		t.Fatalf("zones is not a dsschema.ListAttribute (got %T)", s.Attributes["zones"])
	}
	if !zones.Computed {
		t.Errorf("zones must be Computed: true")
	}
	if zones.Optional || zones.Required {
		t.Errorf("zones must be Computed-only (a data source has no Optional/Required distinction for output attributes)")
	}

	headNode, ok := s.Attributes["head_node"].(dsschema.SingleNestedAttribute)
	if !ok {
		t.Fatalf("head_node is not a dsschema.SingleNestedAttribute (got %T)", s.Attributes["head_node"])
	}
	if !headNode.Computed {
		t.Errorf("head_node must be Computed: true")
	}

	// The data source's node attributes must include required_resources
	// (CC1's rename), not physical_resources - a regression here would mean
	// the data-source schema drifted from CC1 independently of the resource.
	if _, present := headNode.Attributes["physical_resources"]; present {
		t.Error("head_node must NOT have a physical_resources attribute on the data source either (CC1)")
	}
	if _, ok := headNode.Attributes["required_resources"]; !ok {
		t.Error("head_node is missing required_resources (CC1)")
	}

	workerNodes, ok := s.Attributes["worker_nodes"].(dsschema.ListNestedAttribute)
	if !ok {
		t.Fatalf("worker_nodes is not a dsschema.ListNestedAttribute (got %T)", s.Attributes["worker_nodes"])
	}
	if !workerNodes.Computed {
		t.Errorf("worker_nodes must be Computed: true")
	}
	if _, ok := workerNodes.NestedObject.Attributes["required_resources"]; !ok {
		t.Error("worker_nodes[] is missing required_resources (CC1)")
	}
}

// TestCloudResourceHardenedFieldsRequireReplace pins task 861aaf10's fix: on
// anyscale_cloud_resource, Update() is a no-op (it re-reads state but never
// calls the API), so any nested file_storage attribute without
// RequiresReplace silently swallows an edit — the plan diff never converges
// because nothing ever tells Terraform the change needs a replace. This
// catches that regression class in milliseconds instead of needing the
// real-AWS-infra acceptance tests (SkipIfNoRealInfra) to ever run.
//
// The kubernetes_config coverage this test originally had is gone along
// with the 5 fields task #8 removed (see
// TestFlattenKubernetesConfig_APIBackedFieldsPopulate for why) - nothing
// left to pin, since the fields don't exist.
//
// anyscale_cloud's identical duplicated file_storage shape is covered
// separately by TestCloudMountTargetsHardenedFieldsRequireReplace below,
// closing the previously-tracked gap (task 02118d55).
func TestCloudResourceHardenedFieldsRequireReplace(t *testing.T) {
	s := schemaOf(t, &CloudResourceResource{})

	fileStorageBlock, ok := s.Blocks["file_storage"].(schema.SingleNestedBlock)
	if !ok {
		t.Fatalf("file_storage is not a schema.SingleNestedBlock (got %T)", s.Blocks["file_storage"])
	}

	t.Run("file_storage.mount_path", func(t *testing.T) {
		mountPath, ok := fileStorageBlock.Attributes["mount_path"].(schema.StringAttribute)
		if !ok {
			t.Fatalf("file_storage.mount_path is not a schema.StringAttribute (got %T)", fileStorageBlock.Attributes["mount_path"])
		}
		if !hasPlanModifierDescription(mountPath.PlanModifiers, descRequiresReplace) {
			t.Errorf("file_storage.mount_path must include stringplanmodifier.RequiresReplace() — same swallowed-edit " +
				"bug as kubernetes_config (task 861aaf10)")
		}
	})

	// mount_targets is now Optional+Computed (see
	// mount_targets_state_compat_test.go for the Block-to-Attribute
	// rationale). This subtest pins the new attribute shape, replacing the
	// old ListNestedBlock pin (task 861aaf10).
	t.Run("file_storage.mount_targets", func(t *testing.T) {
		mountTargets, ok := fileStorageBlock.Attributes["mount_targets"].(schema.ListNestedAttribute)
		if !ok {
			t.Fatalf("file_storage.mount_targets is not a schema.ListNestedAttribute (got %T)", fileStorageBlock.Attributes["mount_targets"])
		}
		if !mountTargets.Computed {
			t.Error("file_storage.mount_targets must be Computed - it self-heals already-imported state via " +
				"UseStateForUnknown instead of staying a frozen, never-recovered null forever")
		}
		if !mountTargets.Optional {
			t.Error("file_storage.mount_targets must remain Optional - a config may still set it explicitly")
		}
		usfuIdx := indexOfListPlanModifierDescription(mountTargets.PlanModifiers, descUseStateForUnknown)
		rrIdx := indexOfListPlanModifierDescription(mountTargets.PlanModifiers, descRequiresReplace)
		if usfuIdx == -1 {
			t.Error("file_storage.mount_targets must include listplanmodifier.UseStateForUnknown()")
		}
		if rrIdx == -1 {
			t.Error("file_storage.mount_targets must include listplanmodifier.RequiresReplace() - the provider has " +
				"no in-place update path for it (task 861aaf10)")
		}
		if usfuIdx != -1 && rrIdx != -1 && usfuIdx > rrIdx {
			t.Errorf("file_storage.mount_targets: UseStateForUnknown (index %d) must be declared BEFORE "+
				"RequiresReplace (index %d) - the backwards order silently reproduces the replace-on-import bug "+
				"this fix exists to close, see indexOfPlanModifierDescription's doc comment", usfuIdx, rrIdx)
		}
	})
}

// TestCloudMountTargetsHardenedFieldsRequireReplace is
// TestCloudResourceHardenedFieldsRequireReplace's sibling for anyscale_cloud
// - closing task 02118d55, the previously-tracked gap where only
// anyscale_cloud_resource had this pinned. anyscale_cloud carries the
// identical duplicated file_storage shape (both resources build their
// schema independently, field-for-field identical), so the same
// swallowed-edit risk applies here too.
func TestCloudMountTargetsHardenedFieldsRequireReplace(t *testing.T) {
	s := schemaOf(t, &CloudResource{})

	fileStorageBlock, ok := s.Blocks["file_storage"].(schema.SingleNestedBlock)
	if !ok {
		t.Fatalf("file_storage is not a schema.SingleNestedBlock (got %T)", s.Blocks["file_storage"])
	}

	t.Run("file_storage.mount_path", func(t *testing.T) {
		mountPath, ok := fileStorageBlock.Attributes["mount_path"].(schema.StringAttribute)
		if !ok {
			t.Fatalf("file_storage.mount_path is not a schema.StringAttribute (got %T)", fileStorageBlock.Attributes["mount_path"])
		}
		if !hasPlanModifierDescription(mountPath.PlanModifiers, descRequiresReplace) {
			t.Errorf("file_storage.mount_path must include stringplanmodifier.RequiresReplace() — same swallowed-edit " +
				"bug as anyscale_cloud_resource (task 861aaf10)")
		}
	})

	t.Run("file_storage.mount_targets", func(t *testing.T) {
		mountTargets, ok := fileStorageBlock.Attributes["mount_targets"].(schema.ListNestedAttribute)
		if !ok {
			t.Fatalf("file_storage.mount_targets is not a schema.ListNestedAttribute (got %T)", fileStorageBlock.Attributes["mount_targets"])
		}
		if !mountTargets.Computed {
			t.Error("file_storage.mount_targets must be Computed - it self-heals already-imported state via " +
				"UseStateForUnknown instead of staying a frozen, never-recovered null forever")
		}
		usfuIdx := indexOfListPlanModifierDescription(mountTargets.PlanModifiers, descUseStateForUnknown)
		rrIdx := indexOfListPlanModifierDescription(mountTargets.PlanModifiers, descRequiresReplace)
		if usfuIdx == -1 || rrIdx == -1 {
			t.Fatalf("file_storage.mount_targets must include both UseStateForUnknown and RequiresReplace (got usfuIdx=%d rrIdx=%d)", usfuIdx, rrIdx)
		}
		if usfuIdx > rrIdx {
			t.Errorf("file_storage.mount_targets: UseStateForUnknown (index %d) must be declared BEFORE "+
				"RequiresReplace (index %d) - same ordering hazard as anyscale_cloud_resource", usfuIdx, rrIdx)
		}
	})
}

// TestKubernetesConfigInertFieldsAreDeprecated (C5's original deprecation
// pin for the 5 fields task #8 removed - see
// TestFlattenKubernetesConfig_APIBackedFieldsPopulate for why) is deleted,
// not rewritten: there is no longer a deprecation state to assert. See
// internal/acctest/warning_diagnostics_acc_test.go,
// TestAccCloudResource_KubernetesConfigRemovedFieldsRejected, for the
// replacement coverage - a removed attribute must hard-error at
// validate-time, not warn, which needs the real terraform-validate flow
// this schema-only test file cannot exercise.

// TestProjectDescriptionRequiresReplaceIfConfigured pins task 452e7154's fix.
// anyscale_project.description is Optional+Computed (the API auto-generates
// a description when omitted), so a plain RequiresReplace() fires on ANY
// change to the value — including a server-generated description changing on
// its own, or an unrelated update (e.g. a collaborator change) that happens
// to trigger a fresh read — forcing a full project replace nobody asked for.
// RequiresReplaceIfConfigured only fires when the user actually configured a
// value, which is the correct trigger. UseStateForUnknown is required
// alongside it so a server-assigned description stays stable across
// subsequent plans instead of looking perpetually unknown.
func TestProjectDescriptionRequiresReplaceIfConfigured(t *testing.T) {
	s := schemaOf(t, &ProjectResource{})

	desc, ok := s.Attributes["description"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("description is not a schema.StringAttribute (got %T)", s.Attributes["description"])
	}

	if hasPlanModifierDescription(desc.PlanModifiers, descRequiresReplace) {
		t.Errorf("description must NOT use plain stringplanmodifier.RequiresReplace() — that forces a full " +
			"project replace on ANY change, including a server-generated description or an unrelated update " +
			"(task 452e7154's regression). Use RequiresReplaceIfConfigured instead.")
	}
	if !hasPlanModifierDescription(desc.PlanModifiers, descRequiresReplaceIfConfigured) {
		t.Errorf("description must include stringplanmodifier.RequiresReplaceIfConfigured() so replacement only " +
			"triggers on a user-configured change, not a server-side one (task 452e7154)")
	}
	if !hasPlanModifierDescription(desc.PlanModifiers, descUseStateForUnknown) {
		t.Errorf("description must include stringplanmodifier.UseStateForUnknown() so a server-assigned value " +
			"stays stable across subsequent plans instead of appearing perpetually unknown")
	}
}

// TestComputeConfigWorkerNodeNameIsServerInferred pins task 451e2845's fix,
// corrected per task 1f2d592f's live finding.
// worker_nodes[].name ships Optional with no Computed and no plan modifier at
// all, but its own description says it "[d]efaults to a human-friendly
// representation of the instance type" when omitted — exactly the
// omit-or-set-explicitly shape TestServerInferredStringAttributesAreComputedWithUseStateForUnknown
// guards elsewhere (compute_stack, cloud_provider, region), just not yet
// applied here. Without Computed, omitting name plans a hard null; the
// server (or the provider's own instance-type-derived fallback) then returns
// a non-null name, and the framework rejects the apply with "Provider
// produced inconsistent result after apply". This is a table of one rather
// than folded into that existing test because worker_nodes[].name is nested
// inside a ListNestedAttribute, not a top-level schema attribute — a
// different access path (worker_nodes[].NestedObject.Attributes) than that
// test's flat s.Attributes[...] lookups.
//
// Requires UseNonNullStateForUnknown specifically, NOT plain
// UseStateForUnknown. This was originally written mechanism-agnostic (before
// either existed in the fix) accepting either UseStateForUnknown or a static
// Default — forge's live update-add-worker-group repro (task 1f2d592f) found
// that plain UseStateForUnknown actively regresses this exact scenario: for
// an update that adds a brand-new list element, the resource has prior state
// but not at that new index, so UseStateForUnknown copies the missing
// element's null state into the plan instead of leaving it unknown, and the
// apply fails the same "Provider produced inconsistent result" way the
// original bug did. A static Default was never viable either, since the
// default value is derived from the sibling instance_type field, not a fixed
// constant. UseNonNullStateForUnknown is the one mechanism that's actually
// correct here — its own doc string names this exact "child of a nested
// attribute that can be null after the resource is created" shape.
func TestComputeConfigWorkerNodeNameIsServerInferred(t *testing.T) {
	s := schemaOf(t, &ComputeConfigResource{})

	workerNodes, ok := s.Attributes["worker_nodes"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("worker_nodes is not a schema.ListNestedAttribute (got %T)", s.Attributes["worker_nodes"])
	}

	name, ok := workerNodes.NestedObject.Attributes["name"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("worker_nodes[].name is not a schema.StringAttribute (got %T)", workerNodes.NestedObject.Attributes["name"])
	}

	if !name.Optional {
		t.Errorf("worker_nodes[].name must be Optional: true (users may omit it and get an instance-type-derived default)")
	}
	if !name.Computed {
		t.Errorf("worker_nodes[].name must be Computed: true — without it, omitting the name plans a hard null " +
			"that can never reconcile against the non-null name the server/provider assigns, producing " +
			"'Provider produced inconsistent result after apply' (task 451e2845)")
	}
	if hasPlanModifierDescription(name.PlanModifiers, descUseStateForUnknown) {
		t.Errorf("worker_nodes[].name must NOT use plain stringplanmodifier.UseStateForUnknown() — for an " +
			"update that adds a brand-new worker group, that modifier copies the missing element's null prior " +
			"state into the plan instead of leaving it unknown, producing 'Provider produced inconsistent " +
			"result after apply' on the new element (task 1f2d592f's regression). Use UseNonNullStateForUnknown instead.")
	}
	if !hasPlanModifierDescription(name.PlanModifiers, descUseNonNullStateForUnknown) {
		t.Errorf("worker_nodes[].name must include stringplanmodifier.UseNonNullStateForUnknown() — the variant " +
			"safe for an attribute nested inside a list that can be null after creation (task 451e2845 + 1f2d592f)")
	}
}

// TestContainerImageBuildStatusDescriptionsMatchAcceptedEnum pins F1's documentation half: all
// three build_status/latest_build_status surfaces must advertise the exact six-value enum
// evaluateBuildStatus actually accepts (see resource_container_image_build_test.go's
// TestEvaluateBuildStatus_AllAcceptedStatuses for the code-side proof of that same enum). Before
// F1, all three MarkdownDescriptions advertised only five values — pending_cancellation was
// missing entirely, not merely misspelled — and used the two-L "cancelled" spelling the backend
// never actually sends.
func TestContainerImageBuildStatusDescriptionsMatchAcceptedEnum(t *testing.T) {
	wantTokens := []string{"pending", "in_progress", "succeeded", "failed", "pending_cancellation", "canceled"}

	assertHasAllTokens := func(t *testing.T, label, desc string) {
		t.Helper()
		for _, token := range wantTokens {
			if !strings.Contains(desc, "`"+token+"`") {
				t.Errorf("%s MarkdownDescription is missing status value `%s`; got: %s", label, token, desc)
			}
		}
	}

	t.Run("container_image_build.build_status", func(t *testing.T) {
		s := schemaOf(t, &ContainerImageBuildResource{})
		attr, ok := s.Attributes["build_status"].(schema.StringAttribute)
		if !ok {
			t.Fatalf("build_status is not a schema.StringAttribute (got %T)", s.Attributes["build_status"])
		}
		assertHasAllTokens(t, "container_image_build.build_status", attr.MarkdownDescription)
	})

	t.Run("container_image.build_status", func(t *testing.T) {
		s := datasourceSchemaOf(t, &ContainerImageDataSource{})
		attr, ok := s.Attributes["build_status"].(dsschema.StringAttribute)
		if !ok {
			t.Fatalf("build_status is not a dsschema.StringAttribute (got %T)", s.Attributes["build_status"])
		}
		assertHasAllTokens(t, "container_image.build_status", attr.MarkdownDescription)
	})

	t.Run("container_images.container_images[].latest_build_status", func(t *testing.T) {
		s := datasourceSchemaOf(t, &ContainerImagesDataSource{})
		list, ok := s.Attributes["container_images"].(dsschema.ListNestedAttribute)
		if !ok {
			t.Fatalf("container_images is not a dsschema.ListNestedAttribute (got %T)", s.Attributes["container_images"])
		}
		attr, ok := list.NestedObject.Attributes["latest_build_status"].(dsschema.StringAttribute)
		if !ok {
			t.Fatalf("latest_build_status is not a dsschema.StringAttribute (got %T)", list.NestedObject.Attributes["latest_build_status"])
		}
		assertHasAllTokens(t, "container_images.container_images[].latest_build_status", attr.MarkdownDescription)
	})
}

// TestMemoryDBMemorystoreFieldsComputedWithCorrectModifierOrder pins the
// Import Round-Trip Gaps memorydb/memorystore fix (Path A): the 3
// backend-derived fields (aws_config.memorydb_cluster_arn,
// aws_config.memorydb_cluster_endpoint, gcp_config.memorystore_endpoint)
// must be Optional+Computed with UseStateForUnknown declared BEFORE
// RequiresReplace - see indexOfPlanModifierDescription's doc comment for
// why the order is load-bearing, not stylistic.
func TestMemoryDBMemorystoreFieldsComputedWithCorrectModifierOrder(t *testing.T) {
	type fieldCase struct {
		block string
		field string
	}
	fields := []fieldCase{
		{"aws_config", "memorydb_cluster_arn"},
		{"aws_config", "memorydb_cluster_endpoint"},
		{"gcp_config", "memorystore_endpoint"},
	}

	for _, tc := range []struct {
		name string
		res  resource.Resource
	}{
		{"anyscale_cloud", &CloudResource{}},
		{"anyscale_cloud_resource", &CloudResourceResource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := schemaOf(t, tc.res)
			for _, fc := range fields {
				fc := fc
				t.Run(fc.block+"."+fc.field, func(t *testing.T) {
					block, ok := s.Blocks[fc.block].(schema.SingleNestedBlock)
					if !ok {
						t.Fatalf("%s is not a schema.SingleNestedBlock (got %T)", fc.block, s.Blocks[fc.block])
					}
					attr, ok := block.Attributes[fc.field].(schema.StringAttribute)
					if !ok {
						t.Fatalf("%s.%s is not a schema.StringAttribute (got %T)", fc.block, fc.field, block.Attributes[fc.field])
					}
					if !attr.Computed {
						t.Errorf("%s.%s must be Computed - it is backend-derived and must self-heal already-imported "+
							"state via UseStateForUnknown, not just be recovered at import", fc.block, fc.field)
					}
					if !attr.Optional {
						t.Errorf("%s.%s must remain Optional - a config may still set it explicitly", fc.block, fc.field)
					}
					usfuIdx := indexOfPlanModifierDescription(attr.PlanModifiers, descUseStateForUnknown)
					rrIdx := indexOfPlanModifierDescription(attr.PlanModifiers, descRequiresReplace)
					if usfuIdx == -1 {
						t.Errorf("%s.%s must include stringplanmodifier.UseStateForUnknown()", fc.block, fc.field)
					}
					if rrIdx == -1 {
						t.Errorf("%s.%s must include stringplanmodifier.RequiresReplace() - it is a creation-time property", fc.block, fc.field)
					}
					if usfuIdx != -1 && rrIdx != -1 && usfuIdx > rrIdx {
						t.Errorf("%s.%s: UseStateForUnknown (index %d) must be declared BEFORE RequiresReplace (index %d) - "+
							"the backwards order silently reproduces the replace-on-import bug this fix exists to close, "+
							"see indexOfPlanModifierDescription's doc comment", fc.block, fc.field, usfuIdx, rrIdx)
					}
				})
			}
		})
	}
}

// TestObjectStorageRegionSemanticEqualWithCorrectModifierOrder pins the
// object_storage.region import replace-loop fix (WORKBENCH "Import gap:
// object_storage.region explicit-equal", OBJECT-STORAGE-REGION-DESIGN-CORRECTION.md
// - Fix C, superseding the withdrawn Optional+Computed design): region stays
// plain Optional (NOT Computed - the backend genuinely has no real value to
// recover for the explicit-equal case, verified directly against product
// source and a live cloud, so Computed/recover-always cannot work), with
// regionSemanticEqualPlanModifier as region's ONLY plan modifier on BOTH
// anyscale_cloud and anyscale_cloud_resource. It replaces
// stringplanmodifier.RequiresReplace() entirely rather than composing
// alongside it - a separate composed RequiresReplace() was tried and
// rejected (see the modifier's own doc comment in cloud_helpers.go): it
// unconditionally flags replace before this modifier's exception has any
// chance to matter, since RequiresReplace's own equality check runs against
// the ORIGINAL plan value, not this modifier's suppressed one. A plain
// presence check for RequiresReplace would therefore be WRONG here - its
// absence is the correct, load-bearing shape, not an oversight.
func TestObjectStorageRegionSemanticEqualWithCorrectModifierOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  resource.Resource
	}{
		{"anyscale_cloud", &CloudResource{}},
		{"anyscale_cloud_resource", &CloudResourceResource{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := schemaOf(t, tc.res)
			objectStorageBlock, ok := s.Blocks["object_storage"].(schema.SingleNestedBlock)
			if !ok {
				t.Fatalf("object_storage is not a schema.SingleNestedBlock (got %T)", s.Blocks["object_storage"])
			}
			region, ok := objectStorageBlock.Attributes["region"].(schema.StringAttribute)
			if !ok {
				t.Fatalf("object_storage.region is not a schema.StringAttribute (got %T)", objectStorageBlock.Attributes["region"])
			}
			if region.Computed {
				t.Error("object_storage.region must NOT be Computed - the backend never returns a real value when " +
					"region equals the resource's own region (verified against product source and a live cloud), so " +
					"there is nothing to recover; the fix is regionSemanticEqualPlanModifier at plan time, not Computed")
			}
			if !region.Optional {
				t.Error("object_storage.region must remain Optional - a config may still set it explicitly, " +
					"including to a value different from the resource's own region")
			}
			if indexOfPlanModifierDescription(region.PlanModifiers, descRegionSemanticEqual) == -1 {
				t.Error("object_storage.region must include regionSemanticEqualPlanModifier{}")
			}
			if indexOfPlanModifierDescription(region.PlanModifiers, descRequiresReplace) != -1 {
				t.Error("object_storage.region must NOT also include a separate stringplanmodifier.RequiresReplace() - " +
					"regionSemanticEqualPlanModifier subsumes it (sets resp.RequiresReplace itself); composing both " +
					"reintroduces the unconditional replace this fix exists to suppress, since the plain " +
					"RequiresReplace() checks the pre-modifier plan value, not the exception this one applies")
			}
		})
	}
}
