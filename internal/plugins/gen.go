// Package plugins provides generation of opencode plugin files from embedded templates.
package plugins

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tomasWade/octl/internal/paths"

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

// renderedFile 是一份渲染完成的待写文件。
type renderedFile struct {
	Name    string
	Content string
}

// RenderedFiles 将内嵌模板中的版本占位符替换为 ProtocolMD5()，返回待写
// 文件列表。daemon 的启动对齐（alignPlugins）与 Generate 共用此渲染结果。
func RenderedFiles() []renderedFile {
	md5Value := ProtocolMD5()
	return []renderedFile{
		{Name: "octl-hook.js", Content: strings.ReplaceAll(hookTemplate, placeholder, md5Value)},
		{Name: "octl-sidebar.tsx", Content: strings.ReplaceAll(sidebarTemplate, placeholder, md5Value)},
	}
}

// writeIfChanged 在磁盘内容与 content 的 md5 不一致时才写盘（临时文件 +
// rename 原子替换）；文件不存在视为不一致直接写。内容相同时跳过写盘，
// 避免无谓的 mtime 变化。
func writeIfChanged(path, content string) error {
	b := []byte(content)
	if existing, err := os.ReadFile(path); err == nil {
		sumExisting := md5.Sum(existing)
		sumNew := md5.Sum(b)
		if sumExisting == sumNew {
			return nil
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// CreateTemp 产物是 0600；对齐旧的 os.WriteFile 行为（0644），避免内容
	// 更新时把既有 0644 文件静默降权（备份工具等跨用户读取会受影响）。
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Generate 将内嵌模板写入 outputDir，并把模板中的版本占位符替换为
// ProtocolMD5()；内容一致时跳过写盘（md5 比对，避免无谓 mtime bump）。
// outputDir 支持 ~ 前缀（shell 不会展开参数中的 ~，这里统一收口）。
func Generate(outputDir string) error {
	outputDir, err := paths.ExpandHome(outputDir)
	if err != nil {
		return fmt.Errorf("generate plugin: resolve output dir: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("generate plugin: %w", err)
	}

	for _, f := range RenderedFiles() {
		path := filepath.Join(outputDir, f.Name)
		if err := writeIfChanged(path, f.Content); err != nil {
			return fmt.Errorf("generate plugin: %w", err)
		}
	}

	return nil
}

// DefaultOutputDir 返回插件默认输出目录 ~/.config/opencode/plugins
// （octl install 的 --output 缺省值，也是 daemon 启动对齐的目标目录）。
func DefaultOutputDir() (string, error) {
	return expandOpencodePath("plugins")
}

// TUIConfigPath 返回 opencode TUI 插件注册文件 ~/.config/opencode/tui.json。
func TUIConfigPath() (string, error) {
	return expandOpencodePath("tui.json")
}

// expandOpencodePath 展开 ~/.config/opencode 下的相对路径。
func expandOpencodePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", rel), nil
}
