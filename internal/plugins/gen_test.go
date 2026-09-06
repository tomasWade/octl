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
