// paths_test.go：验证 octl 自有路径与旧路径的派生关系。
package paths

import (
	"path/filepath"
	"testing"
)

func TestDerive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	wantDir := filepath.Join(home, ".local", "share", "octl")
	if got, err := Dir(); err != nil || got != wantDir {
		t.Fatalf("Dir() = %q, %v; want %q", got, err, wantDir)
	}
	for _, tc := range []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"SocketPath", SocketPath, filepath.Join(wantDir, "octl.sock")},
		{"StatePath", StatePath, filepath.Join(wantDir, "state.json")},
		{"DailyDir", DailyDir, filepath.Join(wantDir, "daily")},
		{"ShadowDBPath", ShadowDBPath, filepath.Join(wantDir, "shadow.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got != tc.want {
				t.Fatalf("%s() = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
