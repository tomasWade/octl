// testmain_test.go：daemon 包测试的全局隔离——底片/讣告默认目录重定向
// 到临时目录，防止 handleDeleteAction 等走 defaultReportDir() 的代码
// 把测试数据写进真实的 ~/.local/share/opencode/daily/。
package daemon

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "octl-daemon-test-report-*")
	if err != nil {
		os.Stderr.WriteString("testmain: tempdir: " + err.Error() + "\n")
		os.Exit(1)
	}
	setDefaultReportDirForTest(dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
