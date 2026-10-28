package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

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
		if imageURIBlockedByArchivedEnvironment(imageURI, err) {
			return "", &archivedImageURIError{imageURI: imageURI}
		}
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

// imageURIBlockedByArchivedEnvironment reports get_or_create's 409 for a registry URI whose
// anonymous cluster environment was archived. The lookup skips archived environments, and the
// create then collides with the archived one's name (cluster_environments_resource.py:521-525),
// so the URI can never be registered again in the organization. Observed on a real backend.
func imageURIBlockedByArchivedEnvironment(imageURI string, err error) bool {
	var statusErr *UnexpectedStatusError
	return !strings.HasPrefix(imageURI, "anyscale/") && errors.As(err, &statusErr) &&
		statusErr.StatusCode == http.StatusConflict && strings.Contains(statusErr.Body, "This name is already taken")
}

// imageURIEnvironmentName is the anonymous cluster environment name the backend derives from a
// registry URI (builds_service.to_cluster_env_name).
func imageURIEnvironmentName(imageURI string) string {
	return regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(imageURI, "-")
}

type archivedImageURIError struct{ imageURI string }

func (e *archivedImageURIError) Error() string {
	return fmt.Sprintf("Anyscale cannot register image_uri %q: it was registered before as the image (cluster "+
		"environment) %q, which has since been archived, and an archived image keeps its name. Use build_id with "+
		"an existing build, or an image_uri with a different tag. To restore the archived image instead, find its "+
		"id with POST /api/v2/application_templates/search (include_archived and include_anonymous set) and call "+
		"POST /api/v2/application_templates/{id}/unarchive; `anyscale image unarchive` does not find images "+
		"registered from a URI.", e.imageURI, imageURIEnvironmentName(e.imageURI))
}

func stringPointerValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
