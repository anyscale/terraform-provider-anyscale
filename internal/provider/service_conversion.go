package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// serviceObservabilityURLsAttrTypes mirrors ServiceObservabilityURLsModel's shape for use with
// types.ObjectValueFrom. Shared by anyscale_service/anyscale_services (this file) and the
// anyscale_service resource (resource_service.go) - one canonical definition, since attr.Type
// (a model-level concept) is identical across the resource/datasource schema packages, unlike
// schema.Attribute itself which is not.
var serviceObservabilityURLsAttrTypes = map[string]attr.Type{
	"service_dashboard_url":                    types.StringType,
	"service_dashboard_embedding_url":          types.StringType,
	"serve_deployment_dashboard_url":           types.StringType,
	"serve_deployment_dashboard_embedding_url": types.StringType,
}

// serviceVersionAttrTypes mirrors ServiceVersionModel's shape (primary_version/canary_version),
// for the same reason and shared the same way as serviceObservabilityURLsAttrTypes above.
var serviceVersionAttrTypes = map[string]attr.Type{
	"id":                 types.StringType,
	"created_at":         types.StringType,
	"version":            types.StringType,
	"current_state":      types.StringType,
	"weight":             types.Int64Type,
	"current_weight":     types.Int64Type,
	"target_weight":      types.Int64Type,
	"build_id":           types.StringType,
	"compute_config_id":  types.StringType,
	"production_job_ids": types.ListType{ElemType: types.StringType},
	"connection_ids":     types.ListType{ElemType: types.StringType},
	"ray_serve_config":   types.StringType,
}

// populateServiceDataSourceModel maps a ServiceResult into the singular anyscale_service data
// source's model. Shared with the plural anyscale_services data source via
// serviceResultToVersionModel/serviceObservabilityURLsToModel/serviceStatusChecklistToModel below,
// since both data sources return the identical item shape (see
// .crystl/quest/CONTRACT_anyscale_service.md).
func populateServiceDataSourceModel(ctx context.Context, m *ServiceDataSourceModel, s *ServiceResult) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	m.ProjectID = types.StringValue(s.ProjectID)
	m.CloudID = types.StringValue(s.CloudID)
	m.Description = types.StringPointerValue(s.Description)
	m.CreatorID = types.StringValue(s.CreatorID)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.EndedAt = types.StringPointerValue(s.EndedAt)
	m.Hostname = types.StringValue(s.Hostname)
	m.BaseURL = types.StringValue(s.BaseURL)
	m.CurrentState = types.StringValue(s.CurrentState)
	m.GoalState = types.StringValue(s.GoalState)
	m.AutoRolloutEnabled = types.BoolValue(s.AutoRolloutEnabled)
	m.IsMultiVersion = types.BoolValue(s.IsMultiVersion)
	m.ErrorMessage = types.StringPointerValue(s.ErrorMessage)

	obsURLsObj, obsDiags := serviceObservabilityURLsToObject(ctx, s.ServiceObservabilityURLs)
	diags.Append(obsDiags...)
	m.ServiceObservabilityURLs = obsURLsObj

	if s.PrimaryVersion != nil {
		primaryVersion, vDiags := serviceVersionResultToModel(ctx, *s.PrimaryVersion)
		diags.Append(vDiags...)
		primaryObj, pObjDiags := types.ObjectValueFrom(ctx, serviceVersionAttrTypes, primaryVersion)
		diags.Append(pObjDiags...)
		m.PrimaryVersion = primaryObj
	} else {
		m.PrimaryVersion = types.ObjectNull(serviceVersionAttrTypes)
	}

	if s.CanaryVersion != nil {
		canaryVersion, cDiags := serviceVersionResultToModel(ctx, *s.CanaryVersion)
		diags.Append(cDiags...)
		m.CanaryVersion = &canaryVersion
	} else {
		m.CanaryVersion = nil
	}

	m.ServiceStatusChecklist = serviceStatusChecklistToModel(s.ServiceStatusChecklist)

	return diags
}

// serviceObservabilityURLsToObject converts a possibly-nil ServiceObservabilityURLsResult into a
// types.Object, using serviceObservabilityURLsAttrTypes as its shape. Both callers (this data
// source, anyscale_service resource) use this so a null service_observability_urls is rendered
// identically wherever it appears.
func serviceObservabilityURLsToObject(ctx context.Context, u *ServiceObservabilityURLsResult) (types.Object, diag.Diagnostics) {
	if u == nil {
		return types.ObjectNull(serviceObservabilityURLsAttrTypes), nil
	}
	model := serviceObservabilityURLsToModel(*u)
	return types.ObjectValueFrom(ctx, serviceObservabilityURLsAttrTypes, model)
}

// serviceObservabilityURLsToModel maps a ServiceObservabilityURLsResult to its tfsdk model. All
// 4 fields are genuinely optional server-side (nullable dashboard URLs), independent of whether
// the containing object itself is present (see serviceObservabilityURLsToObject for that).
func serviceObservabilityURLsToModel(u ServiceObservabilityURLsResult) ServiceObservabilityURLsModel {
	return ServiceObservabilityURLsModel{
		ServiceDashboardURL:                  types.StringPointerValue(u.ServiceDashboardURL),
		ServiceDashboardEmbeddingURL:         types.StringPointerValue(u.ServiceDashboardEmbeddingURL),
		ServeDeploymentDashboardURL:          types.StringPointerValue(u.ServeDeploymentDashboardURL),
		ServeDeploymentDashboardEmbeddingURL: types.StringPointerValue(u.ServeDeploymentDashboardEmbeddingURL),
	}
}

// serviceVersionResultToModel maps a ServiceVersionResult (used for both primary_version and
// canary_version) to its tfsdk model.
//
// RayServeConfig is required upstream (always present, never JSON null - confirmed against
// ProductionServiceV2VersionModel directly, not assumed), so this always produces a non-null
// string, unlike the other nullable fields on this struct.
func serviceVersionResultToModel(ctx context.Context, v ServiceVersionResult) (ServiceVersionModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	productionJobIDs, pDiags := types.ListValueFrom(ctx, types.StringType, v.ProductionJobIDs)
	diags.Append(pDiags...)

	var connectionIDs types.List
	if v.ConnectionIDs == nil {
		connectionIDs = types.ListNull(types.StringType)
	} else {
		var cDiags diag.Diagnostics
		connectionIDs, cDiags = types.ListValueFrom(ctx, types.StringType, v.ConnectionIDs)
		diags.Append(cDiags...)
	}

	model := ServiceVersionModel{
		ID:               types.StringValue(v.ID),
		CreatedAt:        types.StringValue(v.CreatedAt),
		Version:          types.StringValue(v.Version),
		CurrentState:     types.StringValue(v.CurrentState),
		Weight:           types.Int64Value(v.Weight),
		CurrentWeight:    types.Int64PointerValue(v.CurrentWeight),
		TargetWeight:     types.Int64PointerValue(v.TargetWeight),
		BuildID:          types.StringValue(v.BuildID),
		ComputeConfigID:  types.StringValue(v.ComputeConfigID),
		ProductionJobIDs: productionJobIDs,
		ConnectionIDs:    connectionIDs,
		RayServeConfig:   types.StringValue(string(v.RayServeConfig)),
	}

	return model, diags
}

// serviceStatusChecklistToModel maps an optional ServiceStatusChecklistResult to its tfsdk model.
// Returns nil when the API reports no checklist (null for terminated services and during the
// brief window before the reconciler's first tick on a brand-new service).
func serviceStatusChecklistToModel(c *ServiceStatusChecklistResult) *ServiceStatusChecklistModel {
	if c == nil {
		return nil
	}

	return &ServiceStatusChecklistModel{
		Shared:     statusChecklistItemsToModel(c.Shared),
		PerVersion: versionChecklistsToModel(c.PerVersion),
	}
}

// statusChecklistItemsToModel maps a slice of StatusChecklistItemResult to its tfsdk model slice.
// Always returns a non-nil (possibly empty) slice, since the schema documents `shared`/`items` as
// "empty (not null) if none" - matching the backend's own default_factory=list on both fields.
func statusChecklistItemsToModel(items []StatusChecklistItemResult) []StatusChecklistItemModel {
	result := make([]StatusChecklistItemModel, 0, len(items))
	for _, item := range items {
		result = append(result, StatusChecklistItemModel{
			Kind:       types.StringValue(item.Kind),
			Label:      types.StringValue(item.Label),
			State:      types.StringValue(item.State),
			Message:    types.StringValue(item.Message),
			VersionID:  types.StringPointerValue(item.VersionID),
			ObservedAt: types.StringPointerValue(item.ObservedAt),
		})
	}
	return result
}

// versionChecklistsToModel maps a slice of VersionChecklistResult to its tfsdk model slice. Always
// returns a non-nil (possibly empty) slice, matching per_version's "empty (not null) if none".
func versionChecklistsToModel(groups []VersionChecklistResult) []VersionChecklistModel {
	result := make([]VersionChecklistModel, 0, len(groups))
	for _, g := range groups {
		result = append(result, VersionChecklistModel{
			VersionID: types.StringValue(g.VersionID),
			Items:     statusChecklistItemsToModel(g.Items),
		})
	}
	return result
}
