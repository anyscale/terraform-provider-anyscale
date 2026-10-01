package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-log/tflogtest"
)

// configureHarness runs AnyscaleProvider.Configure with a hand-built provider
// config. token and apiURL are tftypes values so a test can pass null, a
// string, or tftypes.UnknownValue.
type configureResult struct {
	resp   *provider.ConfigureResponse
	client *Client
	logs   string
}

func runConfigure(t *testing.T, token, apiURL any) configureResult {
	t.Helper()

	p := &AnyscaleProvider{version: "test"}
	schemaResp := &provider.SchemaResponse{}
	p.Schema(context.Background(), provider.SchemaRequest{}, schemaResp)

	raw := tftypes.NewValue(
		tftypes.Object{AttributeTypes: map[string]tftypes.Type{
			"api_url": tftypes.String,
			"token":   tftypes.String,
		}},
		map[string]tftypes.Value{
			"api_url": tftypes.NewValue(tftypes.String, apiURL),
			"token":   tftypes.NewValue(tftypes.String, token),
		},
	)

	var buf bytes.Buffer
	ctx := tflogtest.RootLogger(context.Background(), &buf)

	resp := &provider.ConfigureResponse{}
	p.Configure(ctx, provider.ConfigureRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: raw},
	}, resp)

	res := configureResult{resp: resp, logs: buf.String()}
	if c, ok := resp.ResourceData.(*Client); ok {
		res.client = c
	}
	return res
}

// isolateCredentials clears every env var Configure reads and points HOME at
// an empty temp dir, so a developer's real credentials never leak into a test.
// It returns the credentials.json path Configure will look at.
func isolateCredentials(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"ANYSCALE_CLI_TOKEN", "ANYSCALE_API_URL", "ANYSCALE_API_HOST", "ANYSCALE_HOST"} {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".anyscale", "credentials.json")
}

func writeCredentialsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireOneError(t *testing.T, res configureResult, summary string) string {
	t.Helper()
	if len(res.resp.Diagnostics) != 1 || !res.resp.Diagnostics.HasError() {
		t.Fatalf("want exactly one error diagnostic, got %v", res.resp.Diagnostics)
	}
	d := res.resp.Diagnostics[0]
	if d.Summary() != summary {
		t.Fatalf("summary = %q, want %q (detail: %s)", d.Summary(), summary, d.Detail())
	}
	return d.Detail()
}

func requireContainsAll(t *testing.T, detail string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(detail, w) {
			t.Errorf("detail should mention %q, got: %s", w, detail)
		}
	}
}

// Positive controls: the sources that resolve today keep resolving.
func TestConfigure_TokenSources(t *testing.T) {
	t.Run("explicit token wins over env", func(t *testing.T) {
		creds := isolateCredentials(t)
		t.Setenv("ANYSCALE_CLI_TOKEN", "env-token")
		writeCredentialsFile(t, creds, `{"cli_token":"file-token"}`)
		res := runConfigure(t, "cfg-token", nil)
		if res.resp.Diagnostics.HasError() || res.client == nil || res.client.Token != "cfg-token" {
			t.Fatalf("diags=%v client=%+v", res.resp.Diagnostics, res.client)
		}
	})
	t.Run("env wins over file", func(t *testing.T) {
		creds := isolateCredentials(t)
		t.Setenv("ANYSCALE_CLI_TOKEN", "env-token")
		writeCredentialsFile(t, creds, `{"cli_token":"file-token"}`)
		res := runConfigure(t, nil, nil)
		if res.resp.Diagnostics.HasError() || res.client == nil || res.client.Token != "env-token" {
			t.Fatalf("diags=%v client=%+v", res.resp.Diagnostics, res.client)
		}
	})
	t.Run("credentials file", func(t *testing.T) {
		creds := isolateCredentials(t)
		writeCredentialsFile(t, creds, `{"cli_token":"file-token"}`)
		res := runConfigure(t, nil, nil)
		if res.resp.Diagnostics.HasError() || res.client == nil || res.client.Token != "file-token" {
			t.Fatalf("diags=%v client=%+v", res.resp.Diagnostics, res.client)
		}
	})
}

// An explicit `token = ""` is treated as unset, matching api_url, so a module
// that passes through an empty variable falls back to the environment.
func TestConfigure_EmptyTokenFallsBack(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("ANYSCALE_CLI_TOKEN", "env-token")
	res := runConfigure(t, "", nil)
	if res.resp.Diagnostics.HasError() {
		t.Fatalf("empty token should fall back to ANYSCALE_CLI_TOKEN, got %v", res.resp.Diagnostics)
	}
	if res.client == nil || res.client.Token != "env-token" {
		t.Fatalf("client = %+v, want token from ANYSCALE_CLI_TOKEN", res.client)
	}
}

func TestConfigure_UnknownToken(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("ANYSCALE_CLI_TOKEN", "env-token")
	res := runConfigure(t, tftypes.UnknownValue, nil)
	detail := requireOneError(t, res, "Unknown Anyscale API Token")
	requireContainsAll(t, detail, "ANYSCALE_CLI_TOKEN")
	if res.client != nil {
		t.Error("no client should be built from an unknown token")
	}
}

func TestConfigure_UnknownAPIURL(t *testing.T) {
	isolateCredentials(t)
	res := runConfigure(t, "cfg-token", tftypes.UnknownValue)
	detail := requireOneError(t, res, "Unknown Anyscale API URL")
	requireContainsAll(t, detail, "ANYSCALE_API_URL")
	if res.client != nil {
		t.Error("an unknown api_url must not silently fall back to the default host")
	}
}

func TestConfigure_NoCredentialsNamesEverySource(t *testing.T) {
	creds := isolateCredentials(t)
	res := runConfigure(t, nil, nil)
	detail := requireOneError(t, res, "No Anyscale API Token Found")
	requireContainsAll(t, detail, "`token`", "ANYSCALE_CLI_TOKEN", creds, "anyscale login")
}

func TestConfigure_InvalidCredentialsFileIsDistinct(t *testing.T) {
	creds := isolateCredentials(t)
	writeCredentialsFile(t, creds, `{not json`)
	detail := requireOneError(t, runConfigure(t, nil, nil), "Invalid Anyscale Credentials File")
	requireContainsAll(t, detail, creds)

	writeCredentialsFile(t, creds, `{"cli_token":""}`)
	detail = requireOneError(t, runConfigure(t, nil, nil), "Invalid Anyscale Credentials File")
	requireContainsAll(t, detail, creds)
}

// The winning token and URL sources are logged, never the token itself.
func TestConfigure_LogsSourceNotSecret(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("ANYSCALE_HOST", "https://example.invalid")
	const secret = "super-secret-token-value"
	t.Setenv("ANYSCALE_CLI_TOKEN", secret)
	res := runConfigure(t, nil, nil)
	if res.resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", res.resp.Diagnostics)
	}
	requireContainsAll(t, res.logs, "ANYSCALE_CLI_TOKEN", "ANYSCALE_HOST")
	if strings.Contains(res.logs, secret) {
		t.Fatalf("token value leaked into the log: %s", res.logs)
	}
}

// A trailing slash on api_url must not produce `//api/v2/...` request paths.
func TestClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientWithToken(srv.URL+"/", "tok")
	resp, err := c.DoRequest(context.Background(), http.MethodGet, "/api/v2/userinfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotPath != "/api/v2/userinfo" {
		t.Fatalf("request path = %q, want /api/v2/userinfo", gotPath)
	}

	// Positive control: a URL without a trailing slash is untouched.
	if got := NewClientWithToken("https://example.invalid", "tok").BaseURL; got != "https://example.invalid" {
		t.Fatalf("BaseURL = %q", got)
	}
}
