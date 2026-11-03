package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// The function that performs an operation owns its "failed to <verb>" prefix;
// a caller wraps only with context the callee does not add. These pin the
// sites that used to repeat listCloudResources' own prefix.

// cloudResourcesBadJSONServer answers GET /api/v2/clouds/cld_x with a minimal
// cloud and the cloud's resource list with a body that is not JSON, so
// listCloudResources fails with a non-status error - the kind whose text
// reaches the diagnostic unchanged.
func cloudResourcesBadJSONServer(t *testing.T, resourcesStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/clouds/cld_x", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"id":"cld_x","name":"c","provider":"AWS","region":"us-east-1"}}`))
	})
	mux.HandleFunc("/api/v2/clouds/cld_x/resources", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(resourcesStatus)
		_, _ = w.Write([]byte(`not json`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestListCloudResourcesPrefixAppearsOnce(t *testing.T) {
	const phrase = "list cloud resources"
	ctx := context.Background()

	t.Run("anyscale_cloud data source", func(t *testing.T) {
		server := cloudResourcesBadJSONServer(t, http.StatusOK)
		d := &CloudDataSource{client: NewClientWithToken(server.URL, "test-token")}
		var config CloudDataSourceModel
		err := d.readCloudIntoModel(ctx, "cld_x", &config)
		if err == nil {
			t.Fatal("want an error from the unparseable resource list, got nil")
		}
		// Surfaced the way Read surfaces it.
		var diags diag.Diagnostics
		AddAPIError(&diags, "read cloud", err)
		if got := strings.Count(diags[0].Detail(), phrase); got != 1 {
			t.Errorf("%q occurs %d times in %q, want 1", phrase, got, diags[0].Detail())
		}
	})

	t.Run("anyscale_cloud_resource read", func(t *testing.T) {
		server := cloudResourcesBadJSONServer(t, http.StatusOK)
		r := &CloudResourceResource{client: NewClientWithToken(server.URL, "test-token")}
		var state CloudResourceResourceModel
		err := r.readCloudResource(ctx, "cld_x", "res", &state, nil)
		if err == nil {
			t.Fatal("want an error from the unparseable resource list, got nil")
		}
		// readCloudResource adds nothing listCloudResources does not already say.
		if msg := err.Error(); !strings.HasPrefix(msg, "failed to "+phrase+": ") {
			t.Errorf("error = %q, want listCloudResources' own text unwrapped", msg)
		}
	})

	t.Run("cloud_iam_mapping primary resource resolution", func(t *testing.T) {
		// A status error: these callers surface err.Error() directly, so the
		// prefix doubled for every failure, not only for non-status ones.
		server := cloudResourcesBadJSONServer(t, http.StatusInternalServerError)
		_, err := resolvePrimaryCloudResourceID(ctx, NewClientWithToken(server.URL, "test-token"), "cld_x")
		if err == nil {
			t.Fatal("want an error from the failed resource list, got nil")
		}
		msg := err.Error()
		if got := strings.Count(msg, phrase); got != 1 {
			t.Errorf("%q occurs %d times in %q, want 1", phrase, got, msg)
		}
		if !strings.Contains(msg, `"cld_x"`) {
			t.Errorf("error %q no longer names the cloud", msg)
		}
	})
}
