// report.go：根包 "octl report" 子命令——请求 daemon 生成底片并落盘。
// 与 query/action 同机制：flag 解析前拦截，一次性 request+response 后退出。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tomasWade/octl/internal/daemon"
)

// runReport 处理 "octl report"：把窗口参数换算后发给 daemon 的 "report"
// 请求（daemon 执行 digest + 骨架拉取 + 落盘），本地只展示结果。
func runReport(args []string) int {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output raw JSON for scripting")
	dateStr := fs.String("date", "", "single day, e.g. 2026-09-04 (local timezone)")
	fromStr := fs.String("from", "", "window start, e.g. 2026-09-01 or 2026-09-01T14:00")
	toStr := fs.String("to", "", "window end (exclusive), defaults to now")
	dir := fs.String("dir", "", "output directory override (daemon-side; default ~/.local/share/opencode/daily)")
	socketPath := fs.String("socket", "", "daemon Unix socket path (default ~/.local/share/opencode/octl.sock)")
	timeoutSec := fs.Int("timeout", 30, "response timeout in seconds")
	fs.Usage = func() { printReportUsage(fs.Output()) }

	// 参数重排：方法在前 flags 在后的自然写法同样适用。
	flags, positional := reorderArgs(args, map[string]bool{"socket": true, "timeout": true, "date": true, "from": true, "to": true, "dir": true})
	_ = fs.Parse(flags) // ExitOnError：出错时已 os.Exit

	if *timeoutSec <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout 必须为正整数")
		return queryExitUsage
	}
	timeout := time.Duration(*timeoutSec) * time.Second

	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "参数过多：%v\n", positional)
		return queryExitUsage
	}

	from, to, err := parseDailyWindow(*dateStr, *fromStr, *toStr, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl report: %v\n", err)
		return queryExitUsage
	}

	resp, err := daemon.QueryReport(*socketPath, from, to, *dir, timeout)
	if err != nil {
		return cliFatal("octl report", err)
	}
	if !resp.Ok {
		return cliFatal("octl report", fmt.Errorf("%s", resp.Error))
	}

	if *asJSON {
		return printReportJSON(resp)
	}
	renderReportResult(os.Stdout, resp.Report)
	return queryExitOK
}

func renderReportResult(w io.Writer, r *daemon.ReportResult) {
	if r == nil {
		fmt.Fprintln(w, "(no report result)")
		return
	}
	fmt.Fprintf(w, "written  %s\n", r.Path)
	fmt.Fprintf(w, "window   [%s, %s)\n",
		time.UnixMilli(r.From).Format("01-02 15:04"), time.UnixMilli(r.To).Format("01-02 15:04"))
	fmt.Fprintf(w, "active %d · new %d · archived %d · sessions %d\n",
		r.Active, r.New, r.Archived, r.Sessions)
}

func printReportJSON(resp *daemon.ResponseMsg) int {
	out := struct {
		Type   string               `json:"type"`
		Ok     bool                 `json:"ok"`
		Report *daemon.ReportResult `json:"report,omitempty"`
	}{Type: resp.Type, Ok: resp.Ok, Report: resp.Report}
	b, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl report: marshal: %v\n", err)
		return queryExitError
	}
	fmt.Println(string(b))
	return queryExitOK
}

// printReportUsage 输出 report 子命令帮助。
func printReportUsage(w io.Writer) {
	fmt.Fprintf(w, `octl report — 生成日报底片（机械事实层）并落盘

用法：
  octl report [flags]

由 daemon 执行：拉取窗口内 daily digest + 每条活跃 session 的用户消息
骨架，渲染 markdown 整体覆盖写入 <dir>/<date>.raw.md。判断与叙事由
日报 skill 在此底片之上完成（覆盖 octl 只出事实的原则）。

窗口（缺省 = 今天，本地时区自然日）：
  --date <day>      单个自然日，如 2026-09-04（补历史日终版）
  --from <time>     窗口起点（--to 缺省为当前时刻）
  --to <time>       窗口终点（不含）；与 --date 互斥
  时间格式：2026-09-04 / 2026-09-04T14:00 / 2026-09-04 14:00

Flags:
  --dir <path>      输出目录（daemon 侧展开 ~；默认 ~/.local/share/opencode/daily）
  --json            输出完整 JSON（stdout 纯 JSON）
  --socket <path>   daemon socket 路径（默认 ~/.local/share/opencode/octl.sock）
  --timeout <sec>   响应超时秒数（默认 30，含骨架拉取）

daemon 也会自动落盘：启动时 + 每 10 分钟检查（当日底片超 4h 未刷新即
覆盖写，另补昨日终版）；本命令用于手动刷新或补写历史日期。

示例：
  octl report                          # 今天（覆盖写）
  octl report --date 2026-09-04        # 补写 9 月 4 日终版
  octl report --from 2026-09-01        # 9-01 至今（多日窗口，文件名取起点日期）

退出码：0 成功；1 运行错误（daemon 未运行/超时/落盘失败）；2 用法错误
`)
}
