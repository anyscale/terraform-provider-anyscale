package provider

import (
	"strings"
	"testing"
)

func TestParseCloudImportID(t *testing.T) {
	for _, tc := range []struct {
		in        string
		wantID    string
		wantEmpty bool
		wantErr   bool
	}{
		{in: "cld_abc", wantID: "cld_abc"},
		{in: "cld_abc:empty", wantID: "cld_abc", wantEmpty: true},
		{in: "cld_abc:other", wantErr: true},
		{in: "cld_abc:EMPTY", wantErr: true},
		{in: ":empty", wantErr: true},
		{in: "cld_abc:empty:empty", wantErr: true},
		{in: "cld_abc:", wantErr: true},
	} {
		id, empty, err := parseCloudImportID(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: got (%q, %t), want an error", tc.in, id, empty)
				continue
			}
			for _, form := range []string{`"<cloud_id>"`, `"<cloud_id>:empty"`} {
				if !strings.Contains(err.Error(), form) {
					t.Errorf("%q: error %q does not name the accepted form %s", tc.in, err, form)
				}
			}
			continue
		}
		if err != nil || id != tc.wantID || empty != tc.wantEmpty {
			t.Errorf("%q: got (%q, %t, %v), want (%q, %t, nil)", tc.in, id, empty, err, tc.wantID, tc.wantEmpty)
		}
	}
}
