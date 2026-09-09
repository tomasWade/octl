// install.go：octl install 的核心逻辑——生成插件文件并把 sidebar 注册进
// ~/.config/opencode/tui.json。注册遵循「只追加，不替换」：已含精确条目
// 时一字节不动；存在但未含时追加进 plugin 数组并保留其他字段与既有条目；
// 异路径的同名条目不碰（不清楚那是不是用户手写的，octl 无权改掉）；JSON
// 损坏时报错退出、不覆盖用户文件。
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tomasWade/octl/internal/paths"
)

// Install 生成插件文件到 outputDir（支持 ~ 前缀），并确保 tui.json 的
// plugin 数组含指向该目录 sidebar 的 file:// 条目。返回给人读的摘要行。
// tui.json 条目要求绝对路径：相对的 --output（如 --output=plugins）会被
// filepath.Abs 解析为 cwd 下的绝对路径，而不是注册出加载不了的相对条目。
func Install(outputDir string) ([]string, error) {
	expanded, err := paths.ExpandHome(outputDir)
	if err != nil {
		return nil, fmt.Errorf("install: resolve output dir: %w", err)
	}
	expanded, err = filepath.Abs(expanded)
	if err != nil {
		return nil, fmt.Errorf("install: absolutize output dir: %w", err)
	}
	if err := Generate(outputDir); err != nil {
		return nil, fmt.Errorf("install: %w", err)
	}

	entry := "file://" + filepath.Join(expanded, "octl-sidebar.tsx")
	tuiJSON, err := TUIConfigPath()
	if err != nil {
		return nil, fmt.Errorf("install: resolve tui.json path: %w", err)
	}
	// tui.json 的父目录可能不存在（全新机器首装），写盘前确保存在。
	if err := os.MkdirAll(filepath.Dir(tuiJSON), 0o755); err != nil {
		return nil, fmt.Errorf("install: mkdir tui.json parent: %w", err)
	}

	raw, err := os.ReadFile(tuiJSON)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("install: read tui.json: %w", err)
		}
		// 文件不存在：写含 $schema 的最小模板。
		tmpl := tuiJSONTemplate(entry)
		if err := writeIfChanged(tuiJSON, tmpl); err != nil {
			return nil, fmt.Errorf("install: write tui.json: %w", err)
		}
		return []string{
			fmt.Sprintf("generated octl-hook.js and octl-sidebar.tsx in %s", expanded),
			fmt.Sprintf("registered sidebar in %s", tuiJSON),
		}, nil
	}

	merged, changed, err := mergePluginEntry(raw, entry)
	if err != nil {
		return nil, fmt.Errorf("install: tui.json: %w", err)
	}
	if !changed {
		return []string{
			fmt.Sprintf("generated octl-hook.js and octl-sidebar.tsx in %s", expanded),
			"tui.json already registers the sidebar entry",
		}, nil
	}
	if err := writeIfChanged(tuiJSON, string(merged)); err != nil {
		return nil, fmt.Errorf("install: write tui.json: %w", err)
	}
	return []string{
		fmt.Sprintf("generated octl-hook.js and octl-sidebar.tsx in %s", expanded),
		fmt.Sprintf("added sidebar entry to %s", tuiJSON),
	}, nil
}

// tuiJSONTemplate 构造 tui.json 不存在时的最小模板（含 $schema）。
func tuiJSONTemplate(entry string) string {
	return fmt.Sprintf("{\n  \"$schema\": \"https://opencode.ai/tui.json\",\n  \"plugin\": [\n    %q\n  ]\n}\n", entry)
}

// mergePluginEntry 把 entry 追加进 tui.json 的 plugin 数组（纯函数，便于
// 表驱动测试）。返回合并后的完整 JSON、是否有变化、错误：
//   - 已含该精确条目 → 原样返回 raw、changed=false（一字节不动）；
//   - 未含 → 追加，保留其他字段与既有条目、changed=true；
//   - JSON 损坏 → 错误（调用方不得写盘）。
func mergePluginEntry(raw []byte, entry string) (out []byte, changed bool, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, false, fmt.Errorf("parse failed: %w", err)
	}

	pluginRaw, ok := top["plugin"]
	if !ok {
		// 无 plugin 数组：新建。
		quoted, err := json.Marshal(entry)
		if err != nil {
			return nil, false, err
		}
		top["plugin"], err = json.Marshal([]json.RawMessage{quoted})
		if err != nil {
			return nil, false, err
		}
	} else {
		var arr []json.RawMessage
		if err := json.Unmarshal(pluginRaw, &arr); err != nil {
			return nil, false, fmt.Errorf("plugin array parse failed: %w", err)
		}
		for _, item := range arr {
			var s string
			if err := json.Unmarshal(item, &s); err == nil && s == entry {
				// 已含精确条目：原样返回，一字节不动。
				return raw, false, nil
			}
		}
		quoted, err := json.Marshal(entry)
		if err != nil {
			return nil, false, err
		}
		arr = append(arr, quoted)
		top["plugin"], err = json.Marshal(arr)
		if err != nil {
			return nil, false, err
		}
	}

	out, err = json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
