package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tomasWade/octl/internal/daemon"
)

// ---------------------------------------------------------------------------
// octl delete / create / fork / send 四个动词子命令：不打开 TUI，一次性向
// daemon 发送 action 并同步等待 result。与 octl query 共享模糊匹配、退出码
// 约定与 Tokyo Night 配色。
// stdout 只输出操作结果（人类可读行或 --json 的纯 JSON），人读信息走 stderr。
// 退出码：0 成功（含用户主动取消）；1 运行错误；2 用法错误。
// ---------------------------------------------------------------------------

// actionOnce 为包级变量，测试可替换为 mock 以隔离真实 socket 交互。
var actionOnce = daemon.ActionOnce

// actionValueFlags 是动作类子命令中"需要值"的 flag 集合，供 reorderArgs
// 正确分离 "位置参数在前、flags 在后" 的自然写法。
var actionValueFlags = map[string]bool{"socket": true, "timeout": true, "dir": true}

// actionDefaultTimeoutSec 是动作类子命令的默认超时：delete 在 daemon 侧
// 同步逐个执行 opencode session delete，批量删除耗时与数量成正比，因此
// 给出比 query 更宽的默认值。
const actionDefaultTimeoutSec = 60

var (
	actionOKStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a"))
	actionFailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e"))
)

// runActionCommand 是动作类子命令的统一入口：按动词分发。
func runActionCommand(verb string, args []string) int {
	switch verb {
	case "delete":
		return runActionDelete(args)
	case "create":
		return runActionCreate(args)
	case "fork":
		return runActionForkSend("fork", args)
	case "send":
		return runActionForkSend("send", args)
	case "purge":
		return runActionPurge(args)
	default:
		fmt.Fprintf(os.Stderr, "未知动作 %q\n", verb)
		return queryExitUsage
	}
}

// newActionFlagSet 构造动作类子命令的公共 flag 集（--json/--socket/--timeout）。
func newActionFlagSet(name string) (*flag.FlagSet, *bool, *string, *int) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output raw JSON for scripting")
	socketPath := fs.String("socket", "", "daemon Unix socket path (default ~/.local/share/octl/octl.sock)")
	timeoutSec := fs.Int("timeout", actionDefaultTimeoutSec, "result timeout in seconds")
	return fs, asJSON, socketPath, timeoutSec
}

// runActionDelete 处理 "octl delete <sessionId>..."：多参数批量删除，
// sessionId 支持模糊匹配（全部输入解析成功才执行）；tty 下默认弹出确认
// 提示（--yes 跳过），非 tty 直接执行。删除请求带 Cascade=true，由 daemon
// 把目标扩展为"自身 + 全部后代"级联删除，不会留下孤儿 session（该字段
// 缺省为 false，TUI/sidebar 等既有客户端行为不受影响）。
func runActionDelete(args []string) int {
	fs, asJSON, socketPath, timeoutSec := newActionFlagSet("delete")
	assumeYes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.Usage = func() { printActionUsage(fs.Output(), "delete") }
	flags, positional := reorderArgs(args, actionValueFlags)
	_ = fs.Parse(flags) // ExitOnError：出错时已 os.Exit

	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "缺少 <sessionId> 参数（支持模糊匹配，可多个）")
		printActionUsage(os.Stderr, "delete")
		return queryExitUsage
	}
	timeout, code := actionTimeout(*timeoutSec)
	if code >= 0 {
		return code
	}

	cands, err := fetchQueryCandidates(*socketPath, timeout, false)
	if err != nil {
		return cliFatal("octl delete", err)
	}
	targets, err := resolveActionTargets(cands, positional)
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl delete: %v\n", err)
		return queryExitError
	}

	if !*assumeYes && stdinIsTerminal() {
		if !confirmAction(os.Stderr, os.Stdin, targets) {
			fmt.Fprintln(os.Stderr, "已取消")
			return queryExitOK
		}
	}

	ids := make([]string, 0, len(targets))
	titles := map[string]string{}
	for _, t := range targets {
		ids = append(ids, t.id)
		titles[t.id] = t.title
	}
	res, err := actionOnce(*socketPath, daemon.ActionMsg{Action: "delete", SessionIDs: ids, Cascade: true}, timeout)
	if err != nil {
		return cliFatal("octl delete", err)
	}
	return renderActionResult(os.Stdout, res, *asJSON, titles)
}

// runActionPurge 处理 "octl purge <sessionId>..."：影子库的唯一真删除
// 出口。目标必须是已从 opencode 删除的 session（daemon 侧校验），因此
// 不做模糊匹配（被删 session 不在活跃列表里），直接收原始 ID。
// purge 不可逆——档案里彻底消失，不留给墓碑——tty 下强制确认。
func runActionPurge(args []string) int {
	fs, asJSON, socketPath, timeoutSec := newActionFlagSet("purge")
	assumeYes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.Usage = func() { printActionUsage(fs.Output(), "purge") }
	flags, positional := reorderArgs(args, actionValueFlags)
	_ = fs.Parse(flags) // ExitOnError：出错时已 os.Exit

	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "缺少 <sessionId> 参数（只收完整 ID，可多个；只允许清理已从 opencode 删除的 session）")
		printActionUsage(os.Stderr, "purge")
		return queryExitUsage
	}
	timeout, code := actionTimeout(*timeoutSec)
	if code >= 0 {
		return code
	}

	if !*assumeYes && stdinIsTerminal() {
		fmt.Fprintf(os.Stderr, "将彻底销毁影子库中的 %d 个 session（不可逆，不留给墓碑）：\n", len(positional))
		for _, id := range positional {
			fmt.Fprintf(os.Stderr, "  - %s\n", id)
		}
		fmt.Fprint(os.Stderr, "确认？[y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line != "y" && line != "yes" {
			fmt.Fprintln(os.Stderr, "已取消")
			return queryExitOK
		}
	}

	res, err := actionOnce(*socketPath, daemon.ActionMsg{Action: "purge", SessionIDs: positional}, timeout)
	if err != nil {
		return cliFatal("octl purge", err)
	}
	return renderActionResult(os.Stdout, res, *asJSON, nil)
}

// runActionCreate 处理 "octl create <message>"：在指定目录（默认当前
// 目录）新建 session 并发送首条消息。
func runActionCreate(args []string) int {
	fs, asJSON, socketPath, timeoutSec := newActionFlagSet("create")
	dir := fs.String("dir", "", "working directory for the new session (default: current directory)")
	fs.Usage = func() { printActionUsage(fs.Output(), "create") }
	flags, positional := reorderArgs(args, actionValueFlags)
	_ = fs.Parse(flags) // ExitOnError：出错时已 os.Exit

	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		fmt.Fprintln(os.Stderr, "create 需要恰好一个 <message> 参数（含空格时请加引号）")
		printActionUsage(os.Stderr, "create")
		return queryExitUsage
	}
	timeout, code := actionTimeout(*timeoutSec)
	if code >= 0 {
		return code
	}

	workDir := *dir
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "octl create: 获取当前目录失败: %v\n", err)
			return queryExitError
		}
		workDir = cwd
	}

	res, err := actionOnce(*socketPath, daemon.ActionMsg{Action: "create", Directory: workDir, Message: positional[0]}, timeout)
	if err != nil {
		return cliFatal("octl create", err)
	}
	return renderActionResult(os.Stdout, res, *asJSON, nil)
}

// runActionForkSend 处理 "octl fork <sessionId> <message>" 与
// "octl send <sessionId> <message>"：sessionId 支持模糊匹配；--dir 缺省时
// 由 daemon 用该 session 记录的 directory 自动补全。
func runActionForkSend(verb string, args []string) int {
	fs, asJSON, socketPath, timeoutSec := newActionFlagSet(verb)
	dir := fs.String("dir", "", "working directory (default: the session's directory from DB)")
	fs.Usage = func() { printActionUsage(fs.Output(), verb) }
	flags, positional := reorderArgs(args, actionValueFlags)
	_ = fs.Parse(flags) // ExitOnError：出错时已 os.Exit

	if len(positional) != 2 || strings.TrimSpace(positional[0]) == "" || strings.TrimSpace(positional[1]) == "" {
		fmt.Fprintf(os.Stderr, "%s 需要 <sessionId> 与 <message> 两个参数（message 含空格时请加引号）\n", verb)
		printActionUsage(os.Stderr, verb)
		return queryExitUsage
	}
	timeout, code := actionTimeout(*timeoutSec)
	if code >= 0 {
		return code
	}

	cands, err := fetchQueryCandidates(*socketPath, timeout, false)
	if err != nil {
		return cliFatal("octl "+verb, err)
	}
	hit, err := resolveQuerySession(cands, positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl %s: %v\n", verb, err)
		return queryExitError
	}

	res, err := actionOnce(*socketPath, daemon.ActionMsg{
		Action:    verb,
		SessionID: hit.id,
		Directory: *dir,
		Message:   positional[1],
	}, timeout)
	if err != nil {
		return cliFatal("octl "+verb, err)
	}
	return renderActionResult(os.Stdout, res, *asJSON, map[string]string{hit.id: hit.title})
}

// actionTimeout 校验 --timeout 并换算 time.Duration；非法时输出错误并
// 返回负值前的退出码（code >= 0 表示应立即退出）。
func actionTimeout(sec int) (time.Duration, int) {
	if sec <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout 必须为正整数")
		return 0, queryExitUsage
	}
	return time.Duration(sec) * time.Second, -1
}

// resolveActionTargets 把多个模糊匹配输入逐个解析成唯一 session；任一
// 输入未命中或歧义则整体失败（不部分执行）。按 ID 去重（不同输入可能
// 解析到同一 session）。
func resolveActionTargets(cands []queryCandidate, inputs []string) ([]queryCandidate, error) {
	seen := map[string]bool{}
	out := make([]queryCandidate, 0, len(inputs))
	for _, in := range inputs {
		hit, err := resolveQuerySession(cands, in)
		if err != nil {
			return nil, fmt.Errorf("参数 %q: %v", in, err)
		}
		if seen[hit.id] {
			continue
		}
		seen[hit.id] = true
		out = append(out, hit)
	}
	return out, nil
}

// renderActionResult 渲染 daemon 的 ResultMsg：逐条输出成功/失败行与汇总；
// 任一条失败（或 result 自身带 Error）退出码为 1。titles 提供可选的
// sessionId → 标题映射用于补充显示。
func renderActionResult(w io.Writer, res *daemon.ResultMsg, asJSON bool, titles map[string]string) int {
	if res.Error != "" {
		fmt.Fprintf(os.Stderr, "octl: 操作失败: %s\n", res.Error)
		return queryExitError
	}
	if asJSON {
		return printQueryJSON(res)
	}

	for _, r := range res.Summary.Results {
		label := displaySessionID(r.SessionID)
		if t := titles[r.SessionID]; t != "" {
			label += "  " + t
		}
		if r.Success {
			fmt.Fprintf(w, "%s %s\n", actionOKStyle.Render("✓"), label)
		} else {
			fmt.Fprintf(w, "%s %s\n  %s\n", actionFailStyle.Render("✗"), label, actionFailStyle.Render(r.Error))
		}
	}
	s := res.Summary
	fmt.Fprintf(w, "%s: %d succeeded, %d failed\n", res.Action, s.Succeeded, s.Failed)
	if s.Failed > 0 {
		return queryExitError
	}
	return queryExitOK
}

// confirmAction 在 stderr 列出删除目标（级联含子 session）并从 stdin 读取
// 一行确认；只有明确的 y/yes 才继续，空回车与其他输入一律取消。
func confirmAction(w io.Writer, r io.Reader, targets []queryCandidate) bool {
	fmt.Fprintf(w, "将删除以下 %d 个 session 及其全部子 session：\n", len(targets))
	for _, t := range targets {
		fmt.Fprintf(w, "  %s  %s\n", displaySessionID(t.id), t.title)
	}
	fmt.Fprint(w, "确认删除？[y/N] ")
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// stdinIsTerminal 判断 stdin 是否为交互终端（字符设备）；非 tty（管道/
// 重定向）时动作类子命令跳过确认提示，保证脚本可用。
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// printActionUsage 输出动作类子命令帮助。
func printActionUsage(w io.Writer, verb string) {
	var usage, desc string
	switch verb {
	case "delete":
		usage = "octl delete <sessionId>... [flags]"
		desc = "删除 session（可多个；sessionId 模糊匹配，唯一命中才执行）。\n" +
			"级联删除目标的全部子 session，不会留下孤儿数据。"
	case "create":
		usage = "octl create <message> [flags]"
		desc = "在指定目录新建 session 并发送首条消息。"
	case "fork":
		usage = "octl fork <sessionId> <message> [flags]"
		desc = "fork 已有 session 并发送新消息；--dir 缺省时用该 session 记录的目录。"
	case "send":
		usage = "octl send <sessionId> <message> [flags]"
		desc = "向已有 session 发送消息；--dir 缺省时用该 session 记录的目录。"
	}
	fmt.Fprintf(w, `Usage: %s

%s

Flags:
  --json            输出完整 JSON（stdout 纯 JSON，供 jq 等脚本消费）
  --socket <path>   daemon socket 路径（默认 ~/.local/share/octl/octl.sock）
  --timeout <sec>   等待结果超时秒数（默认 %d）
`, usage, desc, actionDefaultTimeoutSec)
	switch verb {
	case "delete":
		fmt.Fprintf(w, "  --yes             跳过删除确认提示\n")
		fmt.Fprintf(w, `
提示：
  删除会级联删除目标的全部子 session；删除为同步执行，批量删除耗时与数量
  成正比。tty 下默认列出目标并要求确认，非 tty（脚本/管道）直接执行。
  需要 daemon 运行中（octl --daemon）。

示例：
  octl delete 5v1n
  octl delete 5v1n --yes
  octl delete abc123 def456 --json
`)
	case "create":
		fmt.Fprintf(w, "  --dir <path>      新 session 的工作目录（默认当前目录）\n")
		fmt.Fprintf(w, `
示例：
  octl create "修复登录 bug"
  octl create "跑一遍测试" --dir ~/code/myproj
`)
	case "fork", "send":
		fmt.Fprintf(w, "  --dir <path>      工作目录（默认取该 session 记录的目录）\n")
		fmt.Fprintf(w, `
示例：
  octl fork 5v1n "换个思路实现"
  octl send 5v1n "继续刚才的任务"
`)
	}
	fmt.Fprintf(w, `
退出码：0 成功（含用户主动取消）；1 运行错误；2 用法错误
`)
}
