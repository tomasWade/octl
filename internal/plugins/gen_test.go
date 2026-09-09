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
