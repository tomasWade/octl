// TestMain 全局夹具：把 state 路径注入临时目录。
//
// 必须全局注入而非逐测试注入：favorite/unfavorite 相关测试经
// handleActionMsg 触发 saveState()，任何漏掉 setStatePathForTest 的
// 用例都会把测试数据写进真实 ~/.local/share/octl/state.json（曾实际
// 发生：测试写入的空收藏文件抢先占位，导致用户收藏丢失）。底片目录
// 无需重定向：writeReport 仅在显式传 dir 的测试中执行。
package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "octl-daemon-test-state")
	if err == nil {
		setStatePathForTest(filepath.Join(dir, "state.json"))
	}
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}
