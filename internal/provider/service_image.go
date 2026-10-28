package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// anyscale_service image_uri: resolved to a build through the same endpoint the CLI uses
// (POST /api/v2/builds/get_or_create_build_from_image_uri). Design:
// docs/decisions/service-inputs-parity/README.md.

// getOrCreateBuildFromImageURIRequest mirrors GetOrCreateBuildFromImageUriRequest. ray_version
// and cluster_env_name are deliberately not sent (see the design doc).
type getOrCreateBuildFromImageURIRequest struct {
	ImageURI            string  `json:"image_uri"`
	RegistryLoginSecret *string `json:"registry_login_secret,omitempty"`
	CloudID             *string `json:"cloud_id,omitempty"`
}

type deploymentInfraResponse struct {
	Result struct {
		DeploymentInfra string `json:"deployment_infra"`
	} `json:"result"`
}

// serviceImageBuildResponse is the part of a DecoratedBuild the image checks need.
type serviceImageBuildResponse struct {
	Result struct {
		ID                  string  `json:"id"`
		Status              string  `json:"status"`
		ErrorMessage        *string `json:"error_message"`
		DockerImageName     *string `json:"docker_image_name"`
		RegistryLoginSecret *string `json:"registry_login_secret"`
		Revision            int     `json:"revision"`
		ApplicationTemplate *struct {
			Name string `json:"name"`
		} `json:"application_template"`
	} `json:"result"`
}

// resolveServiceImageBuild returns the build ID image_uri resolves to. Like the CLI, it sends
// cloud_id only on an Azure control plane, where the endpoint requires it; elsewhere cloud_id
// scopes the environment lookups and is left out.
func resolveServiceImageBuild(ctx context.Context, client *Client, imageURI string, secret types.String, computeConfigID string) (string, error) {
	req := getOrCreateBuildFromImageURIRequest{ImageURI: imageURI, RegistryLoginSecret: secret.ValueStringPointer()}

	infra, err := DoRequestAndParse[deploymentInfraResponse](ctx, client, "GET", "/api/v2/workos/deployment_infra", nil, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("get deployment infrastructure: %w", err)
	}
	if infra.Result.DeploymentInfra == "azure" {
		cc, err := DoRequestAndParse[computeTemplateResponse](ctx, client, "GET",
			"/api/v2/compute_templates/"+url.PathEscape(computeConfigID), nil, http.StatusOK)
		if err != nil {
			return "", fmt.Errorf("get compute config %s for its cloud: %w", computeConfigID, err)
		}
		cloudID := cc.Result.Config.CloudID
		req.CloudID = &cloudID
	}

	body, err := MarshalRequestBody(req)
	if err != nil {
		return "", err
	}
	build, err := DoRequestAndParse[serviceImageBuildResponse](ctx, client, "POST",
		"/api/v2/builds/get_or_create_build_from_image_uri", body, http.StatusOK)
	if err != nil {
		return "", fmt.Errorf("resolve image_uri %q to a build: %w", imageURI, err)
	}
	if build.Result.Status == "failed" {
		reason := "no error message"
		if build.Result.ErrorMessage != nil {
			reason = *build.Result.ErrorMessage
		}
		return "", fmt.Errorf("image_uri %q resolved to build %s, which failed: %s", imageURI, build.Result.ID, reason)
	}
	tflog.Info(ctx, "Resolved service image_uri", map[string]any{"image_uri": imageURI, "build_id": build.Result.ID, "status": build.Result.Status})
	return build.Result.ID, nil
}

// serviceBuildSatisfiesImage reports whether build buildID is what imageURI (with secret) refers
// to, without creating anything: the build's image is the URI (registry images and Anyscale's
// base images) with the same registry secret, or the URI is the build's
// anyscale/image/<name>:<revision> alias. ok=false means the build could not be read.
func serviceBuildSatisfiesImage(ctx context.Context, client *Client, buildID, imageURI string, secret types.String) (satisfied, ok bool) {
	// The GET has been observed to answer 201.
	b, err := DoRequestAndParse[serviceImageBuildResponse](ctx, client, "GET",
		"/api/v2/builds/"+url.PathEscape(buildID), nil, http.StatusOK, http.StatusCreated)
	if err != nil {
		tflog.Warn(ctx, "Could not read the service's build to compare with image_uri", map[string]any{
			"build_id": buildID, "error": err.Error(),
		})
		return false, false
	}
	r := b.Result
	if r.DockerImageName != nil && *r.DockerImageName == imageURI &&
		stringPointerValue(r.RegistryLoginSecret) == secret.ValueString() {
		return true, true
	}
	if r.ApplicationTemplate != nil && imageURI == "anyscale/image/"+r.ApplicationTemplate.Name+":"+strconv.Itoa(r.Revision) {
		return true, true
	}
	return false, true
}

func stringPointerValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
