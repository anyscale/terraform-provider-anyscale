package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// TestClientFromProviderData pins the Configure contract every resource, data
// source and ephemeral resource shares: nil provider data is a silent no-op,
// a wrong type is an error naming that type, and a *Client is returned as is.
func TestClientFromProviderData(t *testing.T) {
	const summary = "Unexpected Resource Configure Type"

	var diags diag.Diagnostics
	if c, ok := clientFromProviderData(nil, summary, &diags); ok || c != nil || diags.HasError() {
		t.Fatalf("nil provider data: got (%v, %v, %v), want (nil, false, no diagnostics)", c, ok, diags)
	}

	diags = nil
	if c, ok := clientFromProviderData("not a client", summary, &diags); ok || c != nil {
		t.Fatalf("wrong type: got (%v, %v), want (nil, false)", c, ok)
	}
	if len(diags) != 1 || diags[0].Summary() != summary ||
		diags[0].Detail() != "Expected *Client, got: string. Please report this issue to the provider developers." {
		t.Fatalf("wrong type: unexpected diagnostics %v", diags)
	}

	diags = nil
	want := &Client{}
	if c, ok := clientFromProviderData(want, summary, &diags); !ok || c != want || diags.HasError() {
		t.Fatalf("*Client: got (%v, %v, %v), want (%v, true, no diagnostics)", c, ok, diags, want)
	}
}
