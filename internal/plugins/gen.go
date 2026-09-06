// Package plugins provides generation of opencode plugin files from embedded templates.
package plugins

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed templates/octl-hook.js
var hookTemplate string

//go:embed templates/octl-sidebar.tsx
var sidebarTemplate string

// placeholder 是模板内嵌的协议版本占位符文本。
const placeholder = "{{OCTL_MD5}}"

// filterLines 剔除模板内容中含有 placeholder 的整行，返回剩余内容。
func filterLines(content string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	first := true
	for _, line := range lines {
		if strings.Contains(line, placeholder) {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		first = false
	}
	return b.String()
}

// computeProtocolMD5 按照固定规则计算协议版本 MD5：
// 先剔除两个模板中的版本占位符行，再按 octl-hook.js、octl-sidebar.tsx 顺序拼接，
// 最后取拼接结果的 MD5 小写 hex 值。
func computeProtocolMD5() string {
	h := md5.New()
	h.Write([]byte(filterLines(hookTemplate)))
	h.Write([]byte(filterLines(sidebarTemplate)))
	return hex.EncodeToString(h.Sum(nil))
}

// ProtocolMD5 返回当前协议版本 MD5 字符串。
func ProtocolMD5() string {
	return computeProtocolMD5()
}

// Generate 将内嵌模板写入 outputDir，并把模板中的版本占位符替换为 ProtocolMD5()。
func Generate(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("generate plugin: %w", err)
	}

	md5Value := ProtocolMD5()
	files := []struct {
		name string
		tpl  string
	}{
		{name: "octl-hook.js", tpl: hookTemplate},
		{name: "octl-sidebar.tsx", tpl: sidebarTemplate},
	}

	for _, f := range files {
		out := strings.ReplaceAll(f.tpl, placeholder, md5Value)
		path := filepath.Join(outputDir, f.name)
		if err := os.WriteFile(path, []byte(out), 0644); err != nil {
			return fmt.Errorf("generate plugin: %w", err)
		}
	}

	return nil
}
