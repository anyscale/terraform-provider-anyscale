package provider

import (
	"testing"
)

func TestAzureControlPlaneHost(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
	}{
		// Each Azure control plane in the CLI's table.
		{"https://console.azure.anyscale.com", "console.azure.anyscale.com"},
		{"https://console.azure.anyscale-staging.com", "console.azure.anyscale-staging.com"},
		{"https://console.gcplane.azure.predeploy.anyscale.dev", "console.gcplane.azure.predeploy.anyscale.dev"},
		// Forms of the same host.
		{"https://CONSOLE.Azure.Anyscale.com", "console.azure.anyscale.com"},
		{"https://console.azure.anyscale.com:443", "console.azure.anyscale.com"},
		{"http://console.azure.anyscale.com:80", "console.azure.anyscale.com"},
		{"https://console.azure.anyscale.com.", "console.azure.anyscale.com"},
		{"https://console.azure.anyscale.com/", "console.azure.anyscale.com"},
		{"https://console.azure.anyscale.com/api/v2?x=1", "console.azure.anyscale.com"},
		{"http://console.azure.anyscale.com", "console.azure.anyscale.com"},

		// Not Azure.
		{"https://console.anyscale.com", ""},
		{"https://console.azure.anyscale.com.evil.example", ""},
		{"https://evil.example/console.azure.anyscale.com", ""},
		{"https://evil.example/?host=console.azure.anyscale.com", ""},
		{"https://azure.anyscale.com", ""},
		{"http://127.0.0.1:8080", ""},
		{"https://console.azure.anyscale.com:8443", ""},
		{"", ""},
		// Schemeless: url.Parse reads it as a path, with no host. Configure does not add a
		// scheme, so such a value already fails every request; it is not classified either.
		{"console.azure.anyscale.com", ""},
	} {
		if got := azureControlPlaneHost(tc.url); got != tc.want {
			t.Errorf("azureControlPlaneHost(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// Classification runs on the resolved URL, whichever source it came from.
func TestAzureControlPlaneHost_AfterSourceResolution(t *testing.T) {
	const azure = "https://console.azure.anyscale.com"
	for _, tc := range []struct {
		name       string
		configured string
		env        map[string]string
		wantSource string
		wantHost   string
	}{
		{"provider argument", azure, nil, "provider configuration", "console.azure.anyscale.com"},
		{"ANYSCALE_API_URL", "", map[string]string{"ANYSCALE_API_URL": azure}, "ANYSCALE_API_URL", "console.azure.anyscale.com"},
		{"ANYSCALE_API_HOST", "", map[string]string{"ANYSCALE_API_HOST": azure}, "ANYSCALE_API_HOST", "console.azure.anyscale.com"},
		{"ANYSCALE_HOST", "", map[string]string{"ANYSCALE_HOST": azure}, "ANYSCALE_HOST", "console.azure.anyscale.com"},
		{"argument wins over env", "https://console.anyscale.com", map[string]string{"ANYSCALE_API_URL": azure}, "provider configuration", ""},
		{"default", "", nil, "default", ""},
		{"schemeless ANYSCALE_HOST", "", map[string]string{"ANYSCALE_HOST": "console.azure.anyscale.com"}, "ANYSCALE_HOST", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"ANYSCALE_API_URL", "ANYSCALE_API_HOST", "ANYSCALE_HOST"} {
				t.Setenv(k, tc.env[k])
			}
			apiURL, source := resolveAPIURL(tc.configured)
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if got := azureControlPlaneHost(apiURL); got != tc.wantHost {
				t.Errorf("azureControlPlaneHost(%q) = %q, want %q", apiURL, got, tc.wantHost)
			}
		})
	}
}
