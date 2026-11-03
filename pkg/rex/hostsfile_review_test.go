package rex_test

import (
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// TestParseConfigReloadRequired: server.config_reload requires generation
// and diagnostics, and diagnostics may be null; a failed reload's output
// still gives its diagnostics.
func TestParseConfigReloadRequired(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{`{"generation": 2, "diagnostics": null}`, true},
		{`{"generation": 0, "diagnostics": []}`, true},
		{`{}`, false},
		{`{"generation": 2}`, false},
		{`{"diagnostics": null}`, false},
		{`{"generation": null, "diagnostics": []}`, false},
	} {
		if _, err := rex.ParseConfigReload([]byte(tc.in)); (err == nil) != tc.ok {
			t.Errorf("ParseConfigReload(%s) = %v, want ok %v", tc.in, err, tc.ok)
		}
	}
	ds, err := rex.ParseConfigDiagnostics(rextest.Fixture("config_check_errors.json"))
	if err != nil || len(ds) != 2 || ds[1].Message != "boom" {
		t.Fatalf("diagnostics = %+v, %v", ds, err)
	}
	if _, err := rex.ParseConfigDiagnostics([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
}
