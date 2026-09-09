// Package paths 是 octl 全部自有数据路径的单一出处。
//
// octl 的数据一律住在自己的目录 ~/.local/share/octl/ 下，不与 opencode
// 的数据目录（~/.local/share/opencode/）混居：octl 对 opencode 数据库
// 只有只读依赖，octl 的 socket、状态、底片、影子库都是自有资产，放
// opencode 目录里既容易误清理，也暗示了不存在的写依赖。
//
// 历史：v0.9 及之前 socket/state/daily 曾默认落在 opencode 目录。
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome 展开 "~" 与 "~/" 前缀为用户主目录；其余路径原样返回。
// CLI 传参（--output、--dir 等）经 shell 手写 ~ 时不展开，各命令入口
// 统一调用本函数收口。
func ExpandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	return path, nil
}

// Dir 返回 octl 自有数据根目录 ~/.local/share/octl。
// 只做路径拼接，不创建目录；需要落盘的调用方自行 MkdirAll。
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "octl"), nil
}

// SocketPath 返回 daemon Unix socket 的默认路径 ~/.local/share/octl/octl.sock。
func SocketPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "octl.sock"), nil
}

// StatePath 返回 daemon 内存态持久化文件 ~/.local/share/octl/state.json。
func StatePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// DailyDir 返回日报底片目录 ~/.local/share/octl/daily。
func DailyDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daily"), nil
}

// ShadowDBPath 返回影子库路径 ~/.local/share/octl/shadow.db。
func ShadowDBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "shadow.db"), nil
}
