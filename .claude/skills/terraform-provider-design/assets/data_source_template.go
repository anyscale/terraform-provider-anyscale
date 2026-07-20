// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &ExampleDataSource{}

func NewExampleDataSource() datasource.DataSource {
	return &ExampleDataSource{}
}

// ExampleDataSource defines the data source implementation.
type ExampleDataSource struct {
	client *http.Client // Replace with your API client
}

// ExampleDataSourceModel describes the data source data model.
type ExampleDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	// Details is a Computed nested object that the upstream API may omit or return as null
	// (e.g. for a resource that has not finished initializing). Model it as types.Object, never
	// as a plain nested struct - a plain struct can only ever be "known", so the framework panics
	// the moment it needs to represent Terraform null for the whole object, not just its fields.
	// A real provider hit exactly this: a data source crashed reading a real, still-transitional
	// resource whose nested status object came back null on the wire. Populate via
	// types.ObjectValueFrom(ctx, exampleDetailsAttrTypes, ...) when present, or
	// types.ObjectNull(exampleDetailsAttrTypes) when the API returns it null/absent - never a
	// bare struct literal assignment.
	Details types.Object `tfsdk:"details"`
}

// ExampleDetailsModel is the nested type for the "details" attribute above. Its own fields use
// ordinary types.String etc. - the null-handling risk is specific to the CONTAINER being
// Computed and nullable, not to its individual fields.
type ExampleDetailsModel struct {
	Status types.String `tfsdk:"status"`
}

var exampleDetailsAttrTypes = map[string]attr.Type{
	"status": types.StringType,
}

// ExampleDetailsResult is the API response shape for the nested object above - a POINTER, so the
// client can represent "the API returned this as null/absent" distinctly from "an empty object."
// Replace with your actual API result type.
type ExampleDetailsResult struct {
	Status string `json:"status"`
}

// exampleDetailsToObject converts the API's nullable *ExampleDetailsResult into a types.Object -
// this is the one function that must exist for any Computed nested attribute the API can return
// null: nil in, types.ObjectNull out; populated in, types.ObjectValueFrom out. Never construct
// ExampleDetailsModel{} directly and assign it to a types.Object-typed field without going through
// ObjectValueFrom - that path is how the real crash happened.
func exampleDetailsToObject(ctx context.Context, d *ExampleDetailsResult) (types.Object, diag.Diagnostics) {
	if d == nil {
		return types.ObjectNull(exampleDetailsAttrTypes), nil
	}
	return types.ObjectValueFrom(ctx, exampleDetailsAttrTypes, ExampleDetailsModel{
		Status: types.StringValue(d.Status),
	})
}

func (d *ExampleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_example"
}

func (d *ExampleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		// This description is used by the documentation generator and the language server.
		MarkdownDescription: "Example data source",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Example identifier",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Example name to look up",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Example description",
			},
			"details": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Example nested status details. Null while the resource is still initializing.",
				Attributes: map[string]schema.Attribute{
					"status": schema.StringAttribute{
						Computed:            true,
						MarkdownDescription: "Example nested status value",
					},
				},
			},
		},
	}
}

func (d *ExampleDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*http.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *http.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

func (d *ExampleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data ExampleDataSourceModel

	// Read Terraform configuration data into the model
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read API call logic here
	// example, err := d.client.GetExampleByName(ctx, data.Name.ValueString())
	// if err != nil {
	//     resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read example, got error: %s", err))
	//     return
	// }

	// Set computed attributes from API response
	// data.ID = types.StringValue(example.ID)
	// data.Description = types.StringValue(example.Description)

	// Nested Computed object: always go through the converter, which handles the API's null case
	// correctly. Never assign an ExampleDetailsModel{} literal directly to data.Details.
	// detailsObj, diags := exampleDetailsToObject(ctx, example.Details)
	// resp.Diagnostics.Append(diags...)
	// data.Details = detailsObj

	// Save data into Terraform state
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
