package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrimarySessionDemoConfigurationCanonicalOnly(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"", true}, {"3d89880a-f45e-4d40-a166-2e6babbc4240", true}, {"00000000-0000-0000-0000-000000000000", false}, {"3D89880A-F45E-4D40-A166-2E6BABBC4240", false}, {" 3d89880a-f45e-4d40-a166-2e6babbc4240", false}, {"*", false}} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if os.WriteFile(path, []byte("[extensions]\nprimary_session_demo_organization_id = \""+tc.value+"\"\n"), 0600) != nil {
				t.Fatal("fixture write")
			}
			_, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
