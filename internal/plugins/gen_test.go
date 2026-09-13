package plugins

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGenerateWritesFiles 验证 Generate 会在输出目录写入两个插件文件。
func TestGenerateWritesFiles(t *testing.T) {
	outDir := t.TempDir()
	if err := Generate(outDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	for _, name := range []string{"octl-hook.js", "octl-sidebar.tsx"} {
		path := filepath.Join(outDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist: %v", path, err)
		}
	}
}

// TestGenerateReplacesPlaceholder 验证占位符已被替换为正确的协议版本常量。
func TestGenerateReplacesPlaceholder(t *testing.T) {
	outDir := t.TempDir()
	if err := Generate(outDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	expected := "const OCTL_PROTOCOL_VERSION = \"" + ProtocolMD5() + "\""
	for _, name := range []string{"octl-hook.js", "octl-sidebar.tsx"} {
		path := filepath.Join(outDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s failed: %v", path, err)
		}
		s := string(content)
		if strings.Contains(s, "{{OCTL_MD5}}") {
			t.Errorf("%s still contains placeholder", name)
		}
		if !strings.Contains(s, expected) {
			t.Errorf("%s does not contain expected version constant %q", name, expected)
		}
	}
}

// TestProtocolMD5Stable 验证 ProtocolMD5 返回稳定的小写 32 位 hex 字符串。
func TestProtocolMD5Stable(t *testing.T) {
	a := ProtocolMD5()
	b := ProtocolMD5()
	if a != b {
		t.Errorf("ProtocolMD5 not stable: %q vs %q", a, b)
	}

	hexRe := regexp.MustCompile("^[0-9a-f]{32}$")
	if !hexRe.MatchString(a) {
		t.Errorf("ProtocolMD5 %q is not a 32-char lowercase hex string", a)
	}
}

// TestGenerateExpandsTilde 验证 --output 传 "~" 前缀时展开为主目录而非
// 在 cwd 下创建字面 "~" 目录（shell 不展开参数中的 ~，曾实际生成 ./~）。
func TestGenerateExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outDir := "~/.config/opencode/plugins"
	if err := Generate(outDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	for _, name := range []string{"octl-hook.js", "octl-sidebar.tsx"} {
		if _, err := os.Stat(filepath.Join(home, ".config/opencode/plugins", name)); err != nil {
			t.Errorf("expected %s under real home: %v", name, err)
		}
	}
	if _, err := os.Stat("~"); err == nil {
		t.Error("literal '~' directory was created in cwd")
	}
}

// TestGenerateMatchesProtocolMD5 验证生成文件中的版本常量值与 ProtocolMD5 一致。
func TestGenerateMatchesProtocolMD5(t *testing.T) {
	outDir := t.TempDir()
	if err := Generate(outDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	expected := ProtocolMD5()
	for _, name := range []string{"octl-hook.js", "octl-sidebar.tsx"} {
		path := filepath.Join(outDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s failed: %v", path, err)
		}
		if !strings.Contains(string(content), expected) {
			t.Errorf("%s does not contain ProtocolMD5 %q", name, expected)
		}
	}
}

// TestGenerateSkipsUnchangedFiles 验证内容一致时 Generate 不重写文件：
// 二次生成后 mtime 保持不变（避免无谓 mtime 变化）。
func TestGenerateSkipsUnchangedFiles(t *testing.T) {
	outDir := t.TempDir()
	if err := Generate(outDir); err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}

	name := filepath.Join(outDir, "octl-hook.js")
	before, err := os.Stat(name)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}

	if err := Generate(outDir); err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	after, err := os.Stat(name)
	if err != nil {
		t.Fatalf("stat after failed: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("file was rewritten despite identical content: mtime %v -> %v", before.ModTime(), after.ModTime())
	}

	// 原子写不留临时文件残留。
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("readdir failed: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestGenerateRewritesChangedFile 验证磁盘内容与渲染结果不一致时覆盖写。
func TestGenerateRewritesChangedFile(t *testing.T) {
	outDir := t.TempDir()
	name := filepath.Join(outDir, "octl-hook.js")
	if err := os.WriteFile(name, []byte("stale content"), 0644); err != nil {
		t.Fatalf("seed stale file failed: %v", err)
	}

	if err := Generate(outDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	if string(got) == "stale content" {
		t.Error("stale content was not replaced")
	}
	if strings.Contains(string(got), "{{OCTL_MD5}}") {
		t.Error("rewritten content still contains placeholder")
	}
}

// TestRenderedOmarchyWidgetFiles 验证 omarchy bar-widget 三件套的渲染：
// 文件名齐全，仅 statusbar.qml 注入协议版本（且无残留占位符），
// manifest.json / statusbar.js 保持模板原文（不含占位符也不含版本值）。
func TestRenderedOmarchyWidgetFiles(t *testing.T) {
	files, err := RenderedOmarchyWidgetFiles()
	if err != nil {
		t.Fatalf("RenderedOmarchyWidgetFiles failed: %v", err)
	}

	want := map[string]bool{"manifest.json": false, "statusbar.qml": false, "statusbar.js": false}
	md5Value := ProtocolMD5()
	for _, f := range files {
		if _, ok := want[f.Name]; !ok {
			t.Errorf("unexpected file %q in omarchy widget set", f.Name)
			continue
		}
		want[f.Name] = true
		if strings.Contains(f.Content, "{{OCTL_MD5}}") {
			t.Errorf("%s still contains placeholder", f.Name)
		}
		if f.Name == "statusbar.qml" {
			if !strings.Contains(f.Content, md5Value) {
				t.Errorf("statusbar.qml does not contain ProtocolMD5 %q", md5Value)
			}
			continue
		}
		if strings.Contains(f.Content, md5Value) {
			t.Errorf("%s unexpectedly contains protocol md5 (only statusbar.qml is versioned)", f.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing omarchy widget file %q", name)
		}
	}
}

// TestGenerateOmarchyWidgetWritesFiles 验证三件套落盘 + 内容一致跳过 +
// 陈旧内容覆盖（对齐 Generate 的 writeIfChanged 语义）。
func TestGenerateOmarchyWidgetWritesFiles(t *testing.T) {
	outDir := t.TempDir()
	if err := GenerateOmarchyWidget(outDir); err != nil {
		t.Fatalf("GenerateOmarchyWidget failed: %v", err)
	}

	files, err := RenderedOmarchyWidgetFiles()
	if err != nil {
		t.Fatalf("RenderedOmarchyWidgetFiles failed: %v", err)
	}
	for _, f := range files {
		path := filepath.Join(outDir, f.Name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file %s: %v", path, err)
		}
	}

	// 幂等：内容一致不重写（mtime 不变）。
	probe := filepath.Join(outDir, "manifest.json")
	before, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateOmarchyWidget(outDir); err != nil {
		t.Fatalf("second GenerateOmarchyWidget failed: %v", err)
	}
	after, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("manifest.json rewritten despite identical content")
	}

	// 陈旧内容被渲染结果覆盖。
	if err := os.WriteFile(probe, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateOmarchyWidget(outDir); err != nil {
		t.Fatalf("third GenerateOmarchyWidget failed: %v", err)
	}
	for _, f := range files {
		if f.Name != "manifest.json" {
			continue
		}
		got, err := os.ReadFile(filepath.Join(outDir, f.Name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != f.Content {
			t.Error("stale manifest.json was not replaced")
		}
	}
}

// TestGenerateOmarchyWidgetExpandsTilde 验证 ~ 前缀展开（与 Generate 同一
// 收口逻辑）。
func TestGenerateOmarchyWidgetExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := GenerateOmarchyWidget("~/somewhere"); err != nil {
		t.Fatalf("GenerateOmarchyWidget failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "somewhere", "manifest.json")); err != nil {
		t.Errorf("expected manifest.json under real home: %v", err)
	}
	if _, err := os.Stat("~"); err == nil {
		t.Error("literal '~' directory was created in cwd")
	}
}

// TestDefaultOmarchyPluginDir 验证默认目录形状与 HOME 展开。
func TestDefaultOmarchyPluginDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultOmarchyPluginDir()
	if err != nil {
		t.Fatalf("DefaultOmarchyPluginDir failed: %v", err)
	}
	want := filepath.Join(home, ".config", "omarchy", "plugins", "octl.sessions")
	if got != want {
		t.Errorf("DefaultOmarchyPluginDir = %q, want %q", got, want)
	}
}
