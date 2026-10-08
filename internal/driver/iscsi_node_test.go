package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureISCSINodeUsesAPIPortal(t *testing.T) {
	for _, tc := range []struct {
		name            string
		code            string
		create, wantErr bool
	}{
		{"existing", "0", false, false},
		{"missing", "21", true, false},
		{"inspection failed", "1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ISCSI_TEST_LOG\"\ncase \"$*\" in *'-o new') exit 0;; esac\nexit \"$ISCSI_TEST_CODE\"\n"
			if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LVMO_ISCSI_HOST_PROC", "")
			t.Setenv("ISCSI_TEST_LOG", log)
			t.Setenv("ISCSI_TEST_CODE", tc.code)
			err := ensureISCSINode(context.Background(), "10.96.0.42:3260", "iqn.test:volume", "lvmo-node")
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := "-m node -T iqn.test:volume -p 10.96.0.42:3260 -I lvmo-node"
			expected := want + "\n"
			if tc.create {
				expected += want + " -o new\n"
			}
			if strings.TrimSpace(string(calls)) != strings.TrimSpace(expected) {
				t.Fatalf("commands:\n%s\nwant:\n%s", calls, expected)
			}
		})
	}
}
