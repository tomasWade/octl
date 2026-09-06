package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/daemon"
)

// fakeDaemon 是动作类子命令测试用的最小 daemon：应答 snapshot/listSessions
// 请求（返回固定候选），把收到的 action 写入 gotAct channel，并按配置回放
// 固定的 progress + result。
type fakeDaemon struct {
	path   string
	ln     net.Listener
	gotAct chan daemon.ActionMsg
	states []daemon.SessionState
	result daemon.ResultMsg
}

// startFakeDaemon 启动 fake daemon 并等待 socket 就绪。
func startFakeDaemon(t *testing.T, states []daemon.SessionState, result daemon.ResultMsg) *fakeDaemon {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "fake.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fd := &fakeDaemon{ln: ln, gotAct: make(chan daemon.ActionMsg, 8), states: states, result: result}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fd.serve(conn)
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.Dial("unix", sockPath); derr == nil {
			c.Close()
			fd.path = sockPath
			return fd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake daemon socket %s not ready", sockPath)
	return nil
}

// serve 处理单条连接上的 subscribe/request/action 消息。
func (fd *fakeDaemon) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var base daemon.BaseMsg
			if json.Unmarshal(line, &base) == nil {
				switch base.Type {
				case "subscribe":
					_ = enc.Encode(daemon.SubscribedMsg{Type: "subscribed"})
				case "request":
					var req daemon.RequestMsg
					_ = json.Unmarshal(line, &req)
					if req.Method == "snapshot" {
						_ = enc.Encode(daemon.ResponseMsg{Type: "response", ID: req.ID, Ok: true, States: fd.states})
					} else {
						_ = enc.Encode(daemon.ResponseMsg{Type: "response", ID: req.ID, Ok: true})
					}
				case "action":
					var act daemon.ActionMsg
					_ = json.Unmarshal(line, &act)
					select {
					case fd.gotAct <- act:
					default:
					}
					_ = enc.Encode(daemon.ProgressMsg{Type: "progress", Action: act.Action})
					_ = enc.Encode(fd.result)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// waitAction 等待 fake daemon 收到一条 action。
func (fd *fakeDaemon) waitAction(t *testing.T) daemon.ActionMsg {
	t.Helper()
	select {
	case act := <-fd.gotAct:
		return act
	case <-time.After(2 * time.Second):
		t.Fatal("fake daemon did not receive an action in time")
		return daemon.ActionMsg{}
	}
}

// fakeStates 是默认候选：ses_s1 / ses_s2。
var fakeStates = []daemon.SessionState{
	{SessionID: "ses_s1", Title: "Alpha"},
	{SessionID: "ses_s2", Title: "Beta"},
}

func okResult(action, sessionID string) daemon.ResultMsg {
	return daemon.ResultMsg{
		Type:   "result",
		Action: action,
		Summary: daemon.Summary{
			Total:     1,
			Succeeded: 1,
			Results: []daemon.Result{
				{SessionID: sessionID, Action: action, Success: true},
			},
		},
	}
}

// TestRunAction_Delete 验证 delete 的模糊匹配、参数重排与 action 发送。
// 测试进程的 stdin（/dev/null）也是字符设备，会误触发确认提示，故传 --yes。
func TestRunAction_Delete(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, okResult("delete", "ses_s1"))

	code := runActionDelete([]string{"s1", "--yes", "--socket", fd.path})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	act := fd.waitAction(t)
	if act.Action != "delete" {
		t.Errorf("action = %q, want delete", act.Action)
	}
	if !act.Cascade {
		t.Error("cascade = false, want true (CLI delete opts in to server-side subtree expansion)")
	}
	if len(act.SessionIDs) != 1 || act.SessionIDs[0] != "ses_s1" {
		t.Errorf("sessionIDs = %v, want [ses_s1]", act.SessionIDs)
	}
}

// TestRunAction_Delete_Batch 验证多参数批量与去重。
func TestRunAction_Delete_Batch(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, okResult("delete", "ses_s1"))

	code := runActionDelete([]string{"s1", "s2", "ses_s1", "--yes", "--socket", fd.path})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	act := fd.waitAction(t)
	// "s1" 与 "ses_s1" 解析到同一 session，去重后应只有两条。
	if len(act.SessionIDs) != 2 {
		t.Errorf("sessionIDs = %v, want 2 unique ids", act.SessionIDs)
	}
}

// TestRunAction_Delete_NoMatch 输入未命中任何 session 时退出码 1 且不发 action。
func TestRunAction_Delete_NoMatch(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, okResult("delete", "x"))

	code := runActionDelete([]string{"zzz", "--socket", fd.path})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	select {
	case act := <-fd.gotAct:
		t.Fatalf("unexpected action sent: %+v", act)
	default:
	}
}

// TestRunAction_Delete_Ambiguous 模糊匹配歧义时退出码 1 且不发 action。
func TestRunAction_Delete_Ambiguous(t *testing.T) {
	states := append(fakeStates, daemon.SessionState{SessionID: "ses_s1b", Title: "Gamma"})
	fd := startFakeDaemon(t, states, okResult("delete", "x"))

	code := runActionDelete([]string{"s1", "--socket", fd.path})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	select {
	case act := <-fd.gotAct:
		t.Fatalf("unexpected action sent: %+v", act)
	default:
	}
}

// TestRunAction_Create 验证 create 发送的 action 字段（Directory 默认 cwd）。
func TestRunAction_Create(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, okResult("create", ""))

	code := runActionCreate([]string{"do it now", "--socket", fd.path})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	act := fd.waitAction(t)
	if act.Action != "create" {
		t.Errorf("action = %q, want create", act.Action)
	}
	if act.Message != "do it now" {
		t.Errorf("message = %q, want %q", act.Message, "do it now")
	}
	cwd, _ := os.Getwd()
	if act.Directory != cwd {
		t.Errorf("directory = %q, want cwd %q", act.Directory, cwd)
	}
}

// TestRunAction_Create_ExplicitDir 验证 --dir 显式覆盖。
func TestRunAction_Create_ExplicitDir(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, okResult("create", ""))

	code := runActionCreate([]string{"hi", "--dir", "/tmp/x", "--socket", fd.path})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	act := fd.waitAction(t)
	if act.Directory != "/tmp/x" {
		t.Errorf("directory = %q, want /tmp/x", act.Directory)
	}
}

// TestRunAction_ForkSend 验证 fork/send 的模糊匹配与 action 字段；
// --dir 缺省时 CLI 不填 Directory（由 daemon 从 DB 补全）。
func TestRunAction_ForkSend(t *testing.T) {
	for _, verb := range []string{"fork", "send"} {
		t.Run(verb, func(t *testing.T) {
			fd := startFakeDaemon(t, fakeStates, okResult(verb, "ses_s2"))

			code := runActionForkSend(verb, []string{"s2", "hello world", "--socket", fd.path})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			act := fd.waitAction(t)
			if act.Action != verb {
				t.Errorf("action = %q, want %q", act.Action, verb)
			}
			if act.SessionID != "ses_s2" {
				t.Errorf("sessionID = %q, want ses_s2", act.SessionID)
			}
			if act.Message != "hello world" {
				t.Errorf("message = %q, want %q", act.Message, "hello world")
			}
			if act.Directory != "" {
				t.Errorf("directory = %q, want empty (daemon auto-resolves)", act.Directory)
			}
		})
	}
}

// TestRunAction_ForkSend_Failure result 带失败条目时退出码 1。
func TestRunAction_ForkSend_Failure(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, daemon.ResultMsg{
		Type:   "result",
		Action: "send",
		Summary: daemon.Summary{
			Total: 1, Failed: 1,
			Results: []daemon.Result{{SessionID: "ses_s2", Action: "send", Success: false, Error: "boom"}},
		},
	})

	code := runActionForkSend("send", []string{"s2", "hi", "--socket", fd.path})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// TestRunAction_ForkSend_ResultError result 自身带 Error 时退出码 1。
func TestRunAction_ForkSend_ResultError(t *testing.T) {
	fd := startFakeDaemon(t, fakeStates, daemon.ResultMsg{Type: "result", Action: "send", Error: "manager not configured"})

	code := runActionForkSend("send", []string{"s2", "hi", "--socket", fd.path})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// TestRunAction_UsageErrors 表驱动验证用法错误统一返回退出码 2。
func TestRunAction_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		run  func(args []string) int
		args []string
	}{
		{"delete without args", runActionDelete, []string{}},
		{"create without args", runActionCreate, []string{}},
		{"create with two args", runActionCreate, []string{"a", "b"}},
		{"create empty message", runActionCreate, []string{"   "}},
		{"fork single arg", func(a []string) int { return runActionForkSend("fork", a) }, []string{"only-one"}},
		{"send three args", func(a []string) int { return runActionForkSend("send", a) }, []string{"s2", "msg", "extra"}},
		{"send empty message", func(a []string) int { return runActionForkSend("send", a) }, []string{"s2", " "}},
		{"delete timeout zero", runActionDelete, []string{"s1", "--timeout", "0"}},
		{"create timeout negative", runActionCreate, []string{"m", "--timeout", "-1"}},
		{"unknown verb", func(a []string) int { return runActionCommand("nope", a) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := tt.run(tt.args); code != queryExitUsage {
				t.Errorf("exit code = %d, want %d", code, queryExitUsage)
			}
		})
	}
}

// TestRenderActionResult 验证结果渲染与退出码。
func TestRenderActionResult(t *testing.T) {
	var b strings.Builder
	code := renderActionResult(&b, &daemon.ResultMsg{
		Type: "result", Action: "delete",
		Summary: daemon.Summary{
			Total: 2, Succeeded: 1, Failed: 1,
			Results: []daemon.Result{
				{SessionID: "ses_a", Action: "delete", Success: true},
				{SessionID: "ses_b", Action: "delete", Success: false, Error: "boom"},
			},
		},
	}, false, map[string]string{"ses_a": "Alpha"})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (has failure)", code)
	}
	out := b.String()
	if !strings.Contains(out, "✓") || !strings.Contains(out, "a  Alpha") {
		t.Errorf("output missing success row with title: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("output missing failure error: %q", out)
	}
	if !strings.Contains(out, "1 succeeded, 1 failed") {
		t.Errorf("output missing summary line: %q", out)
	}

	b.Reset()
	okRes := okResult("create", "ses_a")
	code = renderActionResult(&b, &okRes, false, nil)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	code = renderActionResult(&b, &daemon.ResultMsg{Type: "result", Action: "send", Error: "manager not configured"}, false, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (result error)", code)
	}
}

// TestConfirmAction 表驱动验证确认提示的输入解析。
func TestConfirmAction(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"  YES  \n", true},
		{"n\n", false},
		{"\n", false},
		{"nope\n", false},
		{"", false},
	}
	targets := []queryCandidate{{id: "ses_s1", title: "Alpha"}}
	for _, tt := range tests {
		var w strings.Builder
		if got := confirmAction(&w, strings.NewReader(tt.input), targets); got != tt.want {
			t.Errorf("confirmAction(%q) = %v, want %v", tt.input, got, tt.want)
		}
		if !strings.Contains(w.String(), "ses_s1") && !strings.Contains(w.String(), "s1") {
			t.Errorf("prompt missing target list: %q", w.String())
		}
	}
}

// TestResolveActionTargets 验证批量输入解析：全部命中才成功、按 ID 去重、
// 任一失败整体失败。
func TestResolveActionTargets(t *testing.T) {
	cands := []queryCandidate{
		{id: "ses_s1", title: "Alpha"},
		{id: "ses_s2", title: "Beta"},
	}

	got, err := resolveActionTargets(cands, []string{"s1", "s2", "ses_s1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("targets = %v, want 2 unique", got)
	}

	if _, err := resolveActionTargets(cands, []string{"s1", "zzz"}); err == nil {
		t.Error("expected error for unmatched input, got nil")
	} else if !strings.Contains(err.Error(), `"zzz"`) {
		t.Errorf("error should mention the failing input: %v", err)
	}
}
