package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/plugins"
)

// TestAlignPlugins_RewritesStale 验证磁盘插件与渲染结果不一致时被覆盖为
// 当前协议版本内容。
func TestAlignPlugins_RewritesStale(t *testing.T) {
	dir := t.TempDir()
	old := pluginsDirOverride
	pluginsDirOverride = dir
	defer func() { pluginsDirOverride = old }()

	for _, f := range plugins.RenderedFiles() {
		if err := os.WriteFile(filepath.Join(dir, f.Name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	alignPlugins()

	for _, f := range plugins.RenderedFiles() {
		got, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		if string(got) != f.Content {
			t.Errorf("%s not aligned to rendered content (len %d vs %d)", f.Name, len(got), len(f.Content))
		}
	}
}

// TestAlignPlugins_SkipsMatching 验证内容一致时不重写（避免无谓 mtime 变化）。
func TestAlignPlugins_SkipsMatching(t *testing.T) {
	dir := t.TempDir()
	old := pluginsDirOverride
	pluginsDirOverride = dir
	defer func() { pluginsDirOverride = old }()

	alignPlugins()
	name := filepath.Join(dir, "octl-hook.js")
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}

	alignPlugins()
	after, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("plugin file rewritten despite matching content: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// TestIntegration_MismatchSubscribeTriggersAlign 验证版本不符的 subscribe
// 被拒绝的同时触发插件对齐兜底（写盘后仍拒绝，旧 sidebar 弹重启按钮由
// 用户重启加载）。
// 关键时序：drift 文件必须在 waitForSocket **之后**写入——启动时的
// alignPlugins 会先把它修好，提前写入就测不到 mismatch 触发路径。
func TestIntegration_MismatchSubscribeTriggersAlign(t *testing.T) {
	dir := t.TempDir()
	old := pluginsDirOverride
	pluginsDirOverride = dir
	defer func() { pluginsDirOverride = old }()

	sm, sockPath, cleanup := newIntegrationSM(t)
	defer cleanup()

	go sm.Run()
	waitForSocket(t, sockPath, time.Second)

	// daemon 就绪后再制造磁盘漂移（模拟运行期文件被手改/删除）。
	hookPath := filepath.Join(dir, "octl-hook.js")
	if err := os.WriteFile(hookPath, []byte("drifted"), 0o644); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	sub := map[string]interface{}{"type": "subscribe", "channels": []string{"view"}, "version": "bogus-version"}
	data, _ := json.Marshal(sub)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}

	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		t.Fatal("no response line from daemon")
	}
	var resp struct {
		Type  string `json:"type"`
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Ok {
		t.Fatal("subscribe with wrong version should be rejected")
	}
	if !strings.Contains(resp.Error, "version mismatch") {
		t.Errorf("error = %q, want version mismatch text", resp.Error)
	}

	// 收到拒绝时对齐已同步完成：漂移文件应已被渲染内容覆盖——只有
	// mismatch 分支能触发这次修复（启动对齐早已跑完）。
	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	var rendered string
	for _, f := range plugins.RenderedFiles() {
		if f.Name == "octl-hook.js" {
			rendered = f.Content
		}
	}
	if string(got) != rendered {
		t.Error("drifted plugin file was not repaired on mismatch")
	}
}
