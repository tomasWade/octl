package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entryFor 返回指向 dir 下 sidebar 的注册条目。
func entryFor(dir string) string {
	return "file://" + filepath.Join(dir, "octl-sidebar.tsx")
}

// TestInstall_NoTUIJSON_WritesTemplate：tui.json 不存在时写含 $schema 的
// 模板与正确 file:// 条目，并生成两个插件文件。
func TestInstall_NoTUIJSON_WritesTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outDir := home + "/.config/opencode/plugins"
	if _, err := Install(outDir); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	for _, name := range []string{"octl-hook.js", "octl-sidebar.tsx"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("plugin file %s missing: %v", name, err)
		}
	}

	tuiJSON := home + "/.config/opencode/tui.json"
	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatalf("tui.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("tui.json not valid JSON: %v", err)
	}
	if got["$schema"] != "https://opencode.ai/tui.json" {
		t.Errorf("$schema = %v, want https://opencode.ai/tui.json", got["$schema"])
	}
	want := entryFor(outDir)
	arr, _ := got["plugin"].([]any)
	if len(arr) != 1 || arr[0] != want {
		t.Errorf("plugin = %v, want [%s]", got["plugin"], want)
	}
}

// TestInstall_AlreadyPresent_NoRewrite：已含精确条目时 tui.json 一字节不动。
func TestInstall_AlreadyPresent_NoRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outDir := home + "/.config/opencode/plugins"
	if _, err := Install(outDir); err != nil {
		t.Fatalf("first Install failed: %v", err)
	}
	tuiJSON := home + "/.config/opencode/tui.json"
	before, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatalf("read tui.json failed: %v", err)
	}

	lines, err := Install(outDir)
	if err != nil {
		t.Fatalf("second Install failed: %v", err)
	}
	if !containsLine(lines, "already registers") {
		t.Errorf("summary missing already-present hint: %v", lines)
	}
	after, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatalf("re-read tui.json failed: %v", err)
	}
	if string(before) != string(after) {
		t.Error("tui.json was rewritten despite entry already present")
	}
}

// TestInstall_AppendKeepsFields：存在但未含时追加，原有字段与既有插件
// 条目保留、JSON 仍合法。
func TestInstall_AppendKeepsFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tuiJSON := home + "/.config/opencode/tui.json"
	if err := os.MkdirAll(filepath.Dir(tuiJSON), 0755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "$schema": "https://opencode.ai/tui.json",
  "theme": "tokyonight",
  "plugin": [
    "file:///home/someoneelse/.config/opencode/plugins/other-plugin.tsx"
  ]
}`
	if err := os.WriteFile(tuiJSON, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := home + "/.config/opencode/plugins"
	if _, err := Install(outDir); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("tui.json not valid JSON after append: %v", err)
	}
	if got["theme"] != "tokyonight" {
		t.Errorf("existing field lost: theme = %v", got["theme"])
	}
	arr, _ := got["plugin"].([]any)
	if len(arr) != 2 {
		t.Fatalf("plugin array = %v, want 2 entries", got["plugin"])
	}
	if arr[0] != "file:///home/someoneelse/.config/opencode/plugins/other-plugin.tsx" {
		t.Errorf("existing entry lost: %v", arr[0])
	}
	if arr[1] != entryFor(outDir) {
		t.Errorf("appended entry = %v, want %s", arr[1], entryFor(outDir))
	}
}

// TestInstall_ForeignSidebarEntry_KeptAndAppended：含异路径 octl-sidebar.tsx
// 条目时照样追加新条目，异路径条目原样保留（不替换语义的回归守卫）。
func TestInstall_ForeignSidebarEntry_KeptAndAppended(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tuiJSON := home + "/.config/opencode/tui.json"
	if err := os.MkdirAll(filepath.Dir(tuiJSON), 0755); err != nil {
		t.Fatal(err)
	}
	foreign := "file:///home/olduser/.config/opencode/plugins/octl-sidebar.tsx"
	seed := `{"plugin": ["` + foreign + `"]}`
	if err := os.WriteFile(tuiJSON, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := home + "/.config/opencode/plugins"
	if _, err := Install(outDir); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	arr, _ := got["plugin"].([]any)
	if len(arr) != 2 {
		t.Fatalf("plugin array = %v, want both foreign and new entries", got["plugin"])
	}
	if arr[0] != foreign {
		t.Errorf("foreign entry was modified: %v", arr[0])
	}
	if arr[1] != entryFor(outDir) {
		t.Errorf("new entry = %v, want %s", arr[1], entryFor(outDir))
	}
}

// TestInstall_CorruptJSON_ReturnsError：JSON 损坏时返回错误且文件内容不变。
func TestInstall_CorruptJSON_ReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tuiJSON := home + "/.config/opencode/tui.json"
	if err := os.MkdirAll(filepath.Dir(tuiJSON), 0755); err != nil {
		t.Fatal(err)
	}
	corrupt := `{"plugin": [broken`
	if err := os.WriteFile(tuiJSON, []byte(corrupt), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(home + "/.config/opencode/plugins"); err == nil {
		t.Fatal("Install should fail on corrupt tui.json")
	}
	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != corrupt {
		t.Error("corrupt tui.json was modified")
	}
}

// TestInstall_CustomOutputDir_TildeExpanded：自定义目录时 file:// 条目指向
// 该目录的绝对路径；~ 前缀展开。
func TestInstall_CustomOutputDir_TildeExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install("~/.config/opencode/custom-plugins"); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	tuiJSON := home + "/.config/opencode/tui.json"
	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := entryFor(home + "/.config/opencode/custom-plugins")
	arr, _ := got["plugin"].([]any)
	if len(arr) != 1 || arr[0] != want {
		t.Errorf("plugin = %v, want [%s]", got["plugin"], want)
	}
	if !strings.HasPrefix(want, "file://"+home) {
		t.Errorf("entry not absolute under home: %s", want)
	}
}

// TestInstall_ExternalOutputDir_CreatesTUIJSONParent：--output 指向 HOME
// 之外的目录、且 ~/.config/opencode 完全不存在时，tui.json 写盘前先建父
// 目录（回归守卫：曾因 CreateTemp 在缺失目录上失败而 install 报错）。
func TestInstall_ExternalOutputDir_CreatesTUIJSONParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outDir := t.TempDir() // HOME 之外

	if _, err := Install(outDir); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	tuiJSON := home + "/.config/opencode/tui.json"
	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatalf("tui.json not written: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	arr, _ := got["plugin"].([]any)
	if len(arr) != 1 || arr[0] != entryFor(outDir) {
		t.Errorf("plugin = %v, want [%s]", got["plugin"], entryFor(outDir))
	}
}

// TestInstall_RelativeOutputDir_Absolutized：相对 --output（如 --output=plugins）
// 必须注册为 cwd 下的绝对路径——相对 file:// 条目 opencode 加载不了
// （回归守卫：曾静默注册出 file://plugins/... 的坏条目并以 0 退出）。
func TestInstall_RelativeOutputDir_Absolutized(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wd := t.TempDir()
	chdir(t, wd)

	if _, err := Install("plugins"); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	tuiJSON := home + "/.config/opencode/tui.json"
	b, err := os.ReadFile(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := entryFor(wd + "/plugins")
	arr, _ := got["plugin"].([]any)
	if len(arr) != 1 || arr[0] != want {
		t.Errorf("plugin = %v, want [%s]", got["plugin"], want)
	}
	if strings.HasPrefix(arr[0].(string), "file://plugins/") {
		t.Errorf("relative file:// entry registered: %v", arr[0])
	}
}

// TestInstall_FilePermissions：生成文件与 tui.json 保持 0644
// （CreateTemp 产物是 0600，写回时显式恢复常规可读权限）。
func TestInstall_FilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := Install(home + "/.config/opencode/plugins"); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	hook := home + "/.config/opencode/plugins/octl-hook.js"
	fi, err := os.Stat(hook)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("plugin file perm = %v, want 0644", fi.Mode().Perm())
	}
	tuiJSON := home + "/.config/opencode/tui.json"
	fi, err = os.Stat(tuiJSON)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("tui.json perm = %v, want 0644", fi.Mode().Perm())
	}
}

// chdir 切换工作目录并在测试结束后恢复。
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func containsLine(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
