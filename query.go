package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tomasWade/octl/internal/daemon"
)

// ---------------------------------------------------------------------------
// octl query 子命令：不打开 TUI，一次性向 daemon 查询并打印结果。
// stdout 只输出查询结果（人类表格或 --json 的纯 JSON），人读信息走 stderr。
// 退出码：0 成功；1 运行错误（daemon 未运行 / 超时 / 查询失败）；2 用法错误。
// ---------------------------------------------------------------------------

const (
	queryExitOK    = 0
	queryExitError = 1
	queryExitUsage = 2
)

// Tokyo Night 配色，与 sidebar 插件保持一致。
var queryStatusColor = map[daemon.SessionStatus]string{
	daemon.StatusPermission: "#e0af68",
	daemon.StatusBusy:       "#7aa2f7",
	daemon.StatusRetry:      "#ff9e64",
	daemon.StatusError:      "#f7768e",
	daemon.StatusIdle:       "#9ece6a",
	daemon.StatusUnknown:    "#565f89",
	daemon.StatusArchived:   "#565f89",
}

var queryDim = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
var queryTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5"))

var queryRoleColor = map[string]string{
	"user":      "#7dcfff",
	"assistant": "#9ece6a",
}

// runQuery 处理 "octl query" 子命令：解析参数、校验方法名后分发给对应
// 查询流程。args 为去掉 "query" 后的参数列表。
func runQuery(args []string) int {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output raw JSON for scripting")
	nums := fs.String("nums", "0", "message selector (messages only): 0 / 1,3,-1 / 3-5 / all")
	userOnly := fs.Bool("user-only", false, "messages only: keep user messages only (skeleton of topics)")
	dateStr := fs.String("date", "", "daily only: single day, e.g. 2026-09-04 (local timezone)")
	fromStr := fs.String("from", "", "daily only: window start, e.g. 2026-09-01 or 2026-09-01T14:00")
	toStr := fs.String("to", "", "daily only: window end (exclusive), defaults to now")
	socketPath := fs.String("socket", "", "daemon Unix socket path (default ~/.local/share/opencode/octl.sock)")
	timeoutSec := fs.Int("timeout", 5, "response timeout in seconds")
	fs.Usage = func() { printQueryUsage(fs.Output()) }
	// Go flag 遇到第一个非 flag 参数即停止解析，而 query 的自然写法是
	// method 在前、flags 在后（如 `octl query messages xx --nums all`）。
	// 先分离重组再交给标准 flag 解析。
	flags, positional := reorderQueryArgs(args)
	fs.Parse(flags)

	if *timeoutSec <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout 必须为正整数")
		return queryExitUsage
	}
	timeout := time.Duration(*timeoutSec) * time.Second

	if len(positional) < 1 {
		fmt.Fprintln(os.Stderr, "缺少 <method> 参数")
		printQueryUsage(os.Stderr)
		return queryExitUsage
	}
	method := positional[0]

	maxPositional := 1
	if method == "messages" {
		maxPositional = 2
	}
	if len(positional) > maxPositional {
		fmt.Fprintf(os.Stderr, "参数过多：%v\n", positional)
		return queryExitUsage
	}

	// --nums/--user-only 仅对 messages 有意义，误用时直接指出而不是静默忽略。
	numsSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "nums", "user-only":
			numsSet = true
		}
	})
	if numsSet && method != "messages" {
		fmt.Fprintln(os.Stderr, "--nums/--user-only 仅对 messages 方法有效")
		return queryExitUsage
	}
	// --date/--from/--to 仅对 daily 有意义，同上。
	dailyFlagSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "date", "from", "to":
			dailyFlagSet = true
		}
	})
	if dailyFlagSet && method != "daily" {
		fmt.Fprintln(os.Stderr, "--date/--from/--to 仅对 daily 方法有效")
		return queryExitUsage
	}

	switch method {
	case "snaps":
		return runQuerySnaps(*socketPath, timeout, *asJSON)
	case "sessions":
		return runQuerySessions(*socketPath, timeout, *asJSON)
	case "messages":
		sessionArg := ""
		if len(positional) > 1 {
			sessionArg = positional[1]
		}
		return runQueryMessages(*socketPath, timeout, *asJSON, *nums, *userOnly, sessionArg)
	case "daily":
		return runQueryDaily(*socketPath, timeout, *asJSON, *dateStr, *fromStr, *toStr)
	default:
		fmt.Fprintf(os.Stderr, "未知方法 %q（可用：snaps | sessions | messages | daily）\n", method)
		return queryExitUsage
	}
}

// reorderQueryArgs 把 "method 在前、flags 在后" 的参数序列分离重组成
// 标准flag 包可解析的形式（flags 在前、位置参数在后）。
func reorderQueryArgs(args []string) (flags, positional []string) {
	return reorderArgs(args, map[string]bool{"nums": true, "socket": true, "timeout": true, "date": true, "from": true, "to": true})
}

// reorderArgs 是 reorderQueryArgs 的通用版本：valueFlags 列出所有"需要值"
// 的 flag 名（空格分隔传值时自动吞并其值）。"--" 之后的内容一律视为位置
// 参数；未知 flag 不吞并后续参数，留给 fs.Parse 报标准错误。
func reorderArgs(args []string, valueFlags map[string]bool) (flags, positional []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			return flags, positional
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if strings.ContainsRune(name, '=') {
			// --flag=value 形式自带值，无需吞并。
			flags = append(flags, a)
			continue
		}
		if valueFlags[name] && i+1 < len(args) {
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		flags = append(flags, a) // 布尔 flag 或未知 flag，由 fs.Parse 决定对错
	}
	return flags, positional
}

// runQuerySnaps 查询全部 session 的实时状态；额外拉一次 listSessions 只为
// 拿 projectID → 显示名的映射（失败时回退为直接显示 projectID）。
func runQuerySnaps(socketPath string, timeout time.Duration, asJSON bool) int {
	resp, err := daemon.QueryOnce(socketPath, "snapshot", "", timeout)
	if err != nil {
		return queryFatal(err)
	}
	if !resp.Ok {
		return queryFatal(errors.New(resp.Error))
	}
	names := map[string]string{}
	if lr, lerr := daemon.QueryOnce(socketPath, "listSessions", "", timeout); lerr == nil && lr.Ok {
		for _, p := range lr.Projects {
			names[p.ProjectID] = p.Name
		}
	}
	if asJSON {
		return printQueryJSON(resp)
	}
	renderSnaps(os.Stdout, resp.States, names)
	return queryExitOK
}

// runQuerySessions 查询 project/session 树（root session + 聚合状态）。
func runQuerySessions(socketPath string, timeout time.Duration, asJSON bool) int {
	resp, err := daemon.QueryOnce(socketPath, "listSessions", "", timeout)
	if err != nil {
		return queryFatal(err)
	}
	if !resp.Ok {
		return queryFatal(errors.New(resp.Error))
	}
	if asJSON {
		return printQueryJSON(resp)
	}
	renderSessions(os.Stdout, resp.Projects)
	return queryExitOK
}

// runQueryMessages 查询指定 session 的对话内容。input 为模糊匹配输入，
// 解析成唯一 session 后再向 daemon 请求消息，最后按 --user-only 过滤与
// --nums 选择器输出。userOnly 在 nums 之前生效：序号基于过滤后的序列，
// "--nums 5" 语义为"第 5 条用户消息"。
func runQueryMessages(socketPath string, timeout time.Duration, asJSON bool, numsSpec string, userOnly bool, input string) int {
	if strings.TrimSpace(input) == "" {
		fmt.Fprintln(os.Stderr, "messages 需要一个 sessionId 参数（支持模糊匹配）")
		return queryExitUsage
	}
	cands, err := fetchQueryCandidates(socketPath, timeout)
	if err != nil {
		return queryFatal(err)
	}
	hit, err := resolveQuerySession(cands, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl query: %v\n", err)
		return queryExitError
	}
	resp, err := daemon.QueryOnce(socketPath, "messages", hit.id, timeout)
	if err != nil {
		return queryFatal(err)
	}
	if !resp.Ok {
		return queryFatal(errors.New(resp.Error))
	}
	groups := groupMessageParts(resp.Messages)
	if userOnly {
		groups = filterUserGroups(groups)
	}
	positions, err := parseNums(numsSpec, len(groups))
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl query: %v\n", err)
		return queryExitUsage
	}
	selected := make([][]daemon.MessagePart, 0, len(positions))
	for _, p := range positions {
		selected = append(selected, groups[p-1])
	}
	if asJSON {
		parts := make([]daemon.MessagePart, 0)
		for _, g := range selected {
			parts = append(parts, g...)
		}
		return printQueryJSON(&daemon.ResponseMsg{Type: "response", Ok: true, Messages: parts})
	}
	renderMessages(os.Stdout, hit, len(groups), selected, userOnly)
	return queryExitOK
}

// queryFatal 统一输出运行期错误并返回退出码；对常见错误附操作提示。
func queryFatal(err error) int {
	return cliFatal("octl query", err)
}

// cliFatal 是 queryFatal 的泛化版本：以 ctx 为错误前缀（如 "octl delete"）。
func cliFatal(ctx string, err error) int {
	switch {
	case errors.Is(err, daemon.ErrDaemonConnect):
		fmt.Fprintf(os.Stderr, "%s: %v\n（daemon 未运行？先用 `octl --daemon` 启动）\n", ctx, err)
	case errors.Is(err, daemon.ErrQueryTimeout):
		fmt.Fprintf(os.Stderr, "%s: %v\n（可用 --timeout 调整等待时长）\n", ctx, err)
	default:
		fmt.Fprintf(os.Stderr, "%s: %v\n", ctx, err)
	}
	return queryExitError
}

// ---------------------------------------------------------------------------
// daily：时间窗口聚合查询
// ---------------------------------------------------------------------------

// dailyTimeLayouts 是 --from/--to/--date 支持的时间格式（本地时区）。
var dailyTimeLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
}

// parseDailyTime 解析单个窗口时间点，兼容三种格式；失败返回错误。
func parseDailyTime(s string) (time.Time, error) {
	for _, layout := range dailyTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q 无法解析为日期（支持 2026-09-04 / 2026-09-04T14:00 / 2026-09-04 14:00）", s)
}

// parseDailyWindow 把 --date/--from/--to 解析为 [from, to) 毫秒窗口：
//   - 三者全空：今天（本地时区自然日）
//   - --date：该自然日；与 --from/--to 互斥
//   - --from [--to]：自定义窗口；缺省 --to 为当前时刻；不支持只给 --to
func parseDailyWindow(dateStr, fromStr, toStr string, now time.Time) (from, to int64, err error) {
	hasDate := strings.TrimSpace(dateStr) != ""
	hasFrom := strings.TrimSpace(fromStr) != ""
	hasTo := strings.TrimSpace(toStr) != ""

	if hasDate && (hasFrom || hasTo) {
		return 0, 0, fmt.Errorf("--date 与 --from/--to 互斥，二选一")
	}
	if hasTo && !hasFrom {
		return 0, 0, fmt.Errorf("--to 需要与 --from 搭配使用")
	}

	switch {
	case hasDate:
		// --date 只接受纯自然日；要时间精度请走 --from/--to，
		// 避免带时间的输入被静默解释为"该时刻起 24 小时"。
		day, perr := time.ParseInLocation("2006-01-02", dateStr, time.Local)
		if perr != nil {
			return 0, 0, fmt.Errorf("--date %q 无法解析为自然日（格式 2026-09-04；带时刻的窗口请用 --from/--to）", dateStr)
		}
		return day.UnixMilli(), day.Add(24 * time.Hour).UnixMilli(), nil
	case hasFrom:
		fromT, perr := parseDailyTime(fromStr)
		if perr != nil {
			return 0, 0, perr
		}
		toT := now
		if hasTo {
			toT, perr = parseDailyTime(toStr)
			if perr != nil {
				return 0, 0, perr
			}
		}
		if fromT.UnixMilli() >= toT.UnixMilli() {
			return 0, 0, fmt.Errorf("--from 必须早于 --to")
		}
		return fromT.UnixMilli(), toT.UnixMilli(), nil
	default:
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		return day.UnixMilli(), day.Add(24 * time.Hour).UnixMilli(), nil
	}
}

// runQueryDaily 查询时间窗口内的活动聚合（新增/活跃/归档/僵尸/卡住 +
// 摘要素材），供 CLI 展示与日报 skill 消费。
func runQueryDaily(socketPath string, timeout time.Duration, asJSON bool, dateStr, fromStr, toStr string) int {
	from, to, err := parseDailyWindow(dateStr, fromStr, toStr, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "octl query: %v\n", err)
		return queryExitUsage
	}
	resp, err := daemon.QueryDaily(socketPath, from, to, timeout)
	if err != nil {
		return queryFatal(err)
	}
	if !resp.Ok {
		return queryFatal(errors.New(resp.Error))
	}
	if asJSON {
		return printQueryJSON(resp)
	}
	if resp.Daily == nil {
		return queryFatal(errors.New("daemon 应答缺少 daily 数据"))
	}
	renderDaily(os.Stdout, resp.Daily)
	return queryExitOK
}

// dailyTimeLabel 渲染窗口内时间点：单日窗口显示 HH:MM，跨日窗口带日期。
func dailyTimeLabel(ms int64, withDate bool) string {
	if ms <= 0 {
		return "-"
	}
	t := time.UnixMilli(ms)
	if withDate {
		return t.Format("01-02 15:04")
	}
	return t.Format("15:04")
}

// renderDaily 渲染人类可读的窗口聚合：按 project 分节，尾部附卡住与僵尸。
func renderDaily(w io.Writer, d *daemon.DailyDigest) {
	multiDay := d.To-d.From >= 24*int64(time.Hour/time.Millisecond)
	fromLabel := time.UnixMilli(d.From).Format("2006-01-02 15:04")
	toLabel := time.UnixMilli(d.To).Format("2006-01-02 15:04")

	fmt.Fprintf(w, "%s  %s  %s\n",
		queryTitleStyle.Render("daily digest"),
		queryDim.Render(fromLabel+" ~ "+toLabel),
		queryDim.Render(fmt.Sprintf("active %d · excerpted %d", d.TotalActive, d.Excerpted)))

	if len(d.Projects) == 0 {
		fmt.Fprintln(w, queryDim.Render("(no activity in window)"))
	}

	for _, p := range d.Projects {
		head := queryTitleStyle.Render(p.Name)
		if p.Name == "" {
			head = queryTitleStyle.Render(p.ProjectID)
		}
		if p.Worktree != "" {
			head += "  " + queryDim.Render("("+ansi.Truncate(p.Worktree, 48, "…")+")")
		}
		fmt.Fprintln(w, head)
		fmt.Fprintf(w, "  %s\n", queryDim.Render(fmt.Sprintf(
			"new %d · active %d · archived %d · cost $%.2f",
			len(p.NewSessions), len(p.ActiveSessions), len(p.ArchivedSessions), p.SessionCostSum)))

		for _, s := range p.ActiveSessions {
			fmt.Fprintf(w, "  ACT  %s  %s~%s  %s\n",
				queryDim.Render(fmt.Sprintf("%3dmsg", s.MsgCount)),
				dailyTimeLabel(s.FirstActivity, multiDay),
				dailyTimeLabel(s.LastActivity, multiDay),
				padCell(ansi.Truncate(s.Title, 40, "…"), 40))
			if s.HasExcerpt {
				if s.FirstUserExcerpt != "" {
					fmt.Fprintln(w, "       "+queryDim.Render("首条: ")+queryDailyExcerpt(s.FirstUserExcerpt))
				}
				if s.LastAssistantExcerpt != "" {
					fmt.Fprintln(w, "       "+queryDim.Render("进展: ")+queryDailyExcerpt(s.LastAssistantExcerpt))
				}
			}
		}
		for _, s := range p.NewSessions {
			fmt.Fprintf(w, "  NEW  %s  %s\n",
				dailyTimeLabel(s.TimeCreated, multiDay),
				padCell(ansi.Truncate(s.Title, 40, "…"), 40))
		}
		for _, s := range p.ArchivedSessions {
			fmt.Fprintf(w, "  ARC  %s  %s\n",
				dailyTimeLabel(s.TimeCreated, multiDay),
				padCell(ansi.Truncate(s.Title, 40, "…"), 40))
		}
	}

	if len(d.StuckStates) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", queryTitleStyle.Render(fmt.Sprintf("STUCK (%d)", len(d.StuckStates))))
		for _, st := range d.StuckStates {
			fmt.Fprintf(w, "  %s %s\n", styledStatus(st.Status), ansi.Truncate(st.Title, 40, "…"))
		}
	}
	if len(d.Zombies) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s\n", queryTitleStyle.Render(fmt.Sprintf("ZOMBIE (%d)", len(d.Zombies))))
		for _, z := range d.Zombies {
			fmt.Fprintf(w, "  %s  %s  %s\n",
				padCell(ansi.Truncate(z.Title, 40, "…"), 40),
				queryDim.Render("idle "+relAgeMs(z.LastActivity)),
				queryDim.Render(displaySessionID(z.SessionID)))
		}
	}
}

// queryDailyExcerpt 把多行摘录压成单行展示（换行替换为空格）。
func queryDailyExcerpt(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", " ⏎ ")
}

func printQueryJSON(v interface{}) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "octl query: 输出 JSON 失败: %v\n", err)
		return queryExitError
	}
	return queryExitOK
}

// ---------------------------------------------------------------------------
// session 模糊匹配
// ---------------------------------------------------------------------------

// queryCandidate 是模糊匹配的候选 session（来自 daemon 的 snapshot 与
// listSessions 两个来源的并集）。
type queryCandidate struct {
	id    string
	title string
}

// fetchQueryCandidates 收集全部已知 session 作为模糊匹配候选：snapshot 覆盖
// daemon 追踪的全部 session（含 subsession），listSessions 补充 DB 中的
// root session（daemon 刚启动、状态尚未完全同步时兜底）。按 ID 去重。
func fetchQueryCandidates(socketPath string, timeout time.Duration) ([]queryCandidate, error) {
	seen := map[string]bool{}
	var out []queryCandidate
	add := func(id, title string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, queryCandidate{id: id, title: title})
	}

	resp, err := daemon.QueryOnce(socketPath, "snapshot", "", timeout)
	if err != nil {
		return nil, err
	}
	if resp.Ok {
		for _, st := range resp.States {
			add(st.SessionID, st.Title)
		}
	}
	if lr, lerr := daemon.QueryOnce(socketPath, "listSessions", "", timeout); lerr == nil && lr.Ok {
		for _, p := range lr.Projects {
			for _, s := range p.Sessions {
				add(s.SessionID, s.Title)
			}
		}
	}
	return out, nil
}

// resolveQuerySession 用输入做不区分大小写的子串匹配：输入和 session ID
// 都先转小写再 Contains（输入带不带 ses_ 前缀均可命中）。恰好 1 个命中才
// 返回；0 个报找不到；多个报歧义并列出候选（最多 20 条）。
func resolveQuerySession(cands []queryCandidate, input string) (queryCandidate, error) {
	q := strings.ToLower(strings.TrimSpace(input))
	var hits []queryCandidate
	for _, c := range cands {
		if strings.Contains(strings.ToLower(c.id), q) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return queryCandidate{}, fmt.Errorf("找不到匹配 %q 的 session", input)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q 匹配到 %d 个 session：\n", input, len(hits))
		for i, h := range hits {
			if i >= 20 {
				fmt.Fprintf(&b, "  …（其余 %d 个省略）\n", len(hits)-20)
				break
			}
			fmt.Fprintf(&b, "  %s  %s\n", displaySessionID(h.id), h.title)
		}
		return queryCandidate{}, errors.New(b.String())
	}
}

// displaySessionID 把 session ID 规范化为展示形态：全小写并去掉 ses_ 前缀。
func displaySessionID(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	return strings.TrimPrefix(s, "ses_")
}

// ---------------------------------------------------------------------------
// --nums 消息选择器
// ---------------------------------------------------------------------------

var (
	queryNumsSingle = regexp.MustCompile(`^-?\d+$`)
	queryNumsRange  = regexp.MustCompile(`^(-?\d+)-(-?\d+)$`)
)

// resolveNumPos 把 --nums 索引换算成 1-based 消息位置并钳制到合法范围：
// 正数从头数（1=第一条）；n<=0 从尾数（0=最后一条，-1=上一条，-2=再上一条）。
// 正数溢出取最后一条，负数溢出取第一条。
func resolveNumPos(n, total int) int {
	p := n
	if n <= 0 {
		p = total + n
	}
	if p < 1 {
		p = 1
	}
	if p > total {
		p = total
	}
	return p
}

// parseNums 解析 --nums 选择器，返回去重后的升序位置列表（1-based）。
// 支持：单值（0/1/-2）、范围（3-5、4--2、-2-6，降序自动交换为升序）、
// 逗号列表（1,3,-1）、关键字 all（全部）。total<=0 时返回空选择。
func parseNums(spec string, total int) ([]int, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("--nums 不能为空")
	}
	if total <= 0 {
		return nil, nil
	}
	set := map[int]bool{}
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		switch {
		case f == "all":
			for i := 1; i <= total; i++ {
				set[i] = true
			}
		case queryNumsSingle.MatchString(f):
			n, _ := strconv.Atoi(f)
			set[resolveNumPos(n, total)] = true
		case queryNumsRange.MatchString(f):
			m := queryNumsRange.FindStringSubmatch(f)
			a, _ := strconv.Atoi(m[1])
			b, _ := strconv.Atoi(m[2])
			pa, pb := resolveNumPos(a, total), resolveNumPos(b, total)
			if pa > pb {
				pa, pb = pb, pa
			}
			for i := pa; i <= pb; i++ {
				set[i] = true
			}
		default:
			return nil, fmt.Errorf("--nums 元素 %q 无法解析（支持 0 / 1 / -2 / 3-5 / 4--2 / all）", f)
		}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

// groupMessageParts 把按时间升序返回的消息 parts 按所属消息（MessageID）
// 分组：同一消息的多个 part 在结果中连续出现，合并为一组。
func groupMessageParts(parts []daemon.MessagePart) [][]daemon.MessagePart {
	var groups [][]daemon.MessagePart
	for _, p := range parts {
		if n := len(groups); n > 0 && groups[n-1][0].MessageID == p.MessageID {
			groups[n-1] = append(groups[n-1], p)
			continue
		}
		groups = append(groups, []daemon.MessagePart{p})
	}
	return groups
}

// filterUserGroups 保留角色为 user 的消息组（主题骨架）。过滤在 --nums
// 之前生效：序号基于过滤后的序列重排，"--nums 5" 语义为"第 5 条用户消息"。
func filterUserGroups(groups [][]daemon.MessagePart) [][]daemon.MessagePart {
	filtered := make([][]daemon.MessagePart, 0, len(groups))
	for _, g := range groups {
		if len(g) > 0 && g[0].Role == "user" {
			filtered = append(filtered, g)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// 人类可读渲染（supervisorctl 风格：无表头、状态彩色、列定宽）
// ---------------------------------------------------------------------------

// queryStatusPriority 决定 snaps 输出中状态的排序：需要关注的在前，同优先
// 级按最近活动时间降序。
var queryStatusPriority = map[daemon.SessionStatus]int{
	daemon.StatusPermission: 0,
	daemon.StatusError:      1,
	daemon.StatusBusy:       2,
	daemon.StatusRetry:      3,
	daemon.StatusIdle:       4,
	daemon.StatusUnknown:    5,
	daemon.StatusArchived:   6,
}

func renderSnaps(w io.Writer, states []daemon.SessionState, projNames map[string]string) {
	if len(states) == 0 {
		fmt.Fprintln(w, queryDim.Render("(no sessions)"))
		return
	}
	sorted := make([]daemon.SessionState, len(states))
	copy(sorted, states)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := queryStatusPriority[sorted[i].Status], queryStatusPriority[sorted[j].Status]
		if pi != pj {
			return pi < pj
		}
		return queryStateTime(sorted[i]).After(queryStateTime(sorted[j]))
	})

	titleW, projW := 0, 0
	for _, st := range sorted {
		titleW = maxInt(titleW, lipgloss.Width(st.Title))
		projW = maxInt(projW, lipgloss.Width(queryProjectLabel(st.ProjectID, projNames)))
	}
	titleW = clampInt(titleW, 8, 40)
	projW = clampInt(projW, 6, 24)

	for _, st := range sorted {
		title := padCell(ansi.Truncate(queryTitleStyle.Render(st.Title), titleW, "…"), titleW)
		proj := padCell(ansi.Truncate(queryDim.Render(queryProjectLabel(st.ProjectID, projNames)), projW, "…"), projW)
		age := fmt.Sprintf("%4s", relAgeMs(queryStateTimeMs(st)))
		fmt.Fprintf(w, "%s %s %s %s  %s\n",
			styledStatus(st.Status), title, proj,
			queryDim.Render(age), queryDim.Render(displaySessionID(st.SessionID)))
	}
}

func renderSessions(w io.Writer, projects []daemon.SidebarProject) {
	if len(projects) == 0 {
		fmt.Fprintln(w, queryDim.Render("(no projects)"))
		return
	}
	titleW := 0
	for _, p := range projects {
		for _, s := range p.Sessions {
			titleW = maxInt(titleW, lipgloss.Width(s.Title))
		}
	}
	titleW = clampInt(titleW, 8, 40)

	for _, p := range projects {
		cnt := fmt.Sprintf("%d session", len(p.Sessions))
		if len(p.Sessions) != 1 {
			cnt += "s"
		}
		glyph := "  "
		if st := queryProjectRowStatus(p); st != "" {
			glyph = st.Glyph() + " "
		}
		head := queryTitleStyle.Render(p.Name) + "  " +
			queryDim.Render("("+ansi.Truncate(p.Worktree, 48, "…")+")") + "  " +
			glyph + queryDim.Render(cnt)
		fmt.Fprintln(w, head)
		for _, s := range p.Sessions {
			title := padCell(ansi.Truncate(s.Title, titleW, "…"), titleW)
			age := fmt.Sprintf("%4s", relAgeMs(s.TimeUpdated))
			fmt.Fprintf(w, "  %s %s %s  %s\n",
				styledStatus(s.RowStatus), title,
				queryDim.Render(age), queryDim.Render(displaySessionID(s.SessionID)))
		}
	}
}

func renderMessages(w io.Writer, c queryCandidate, total int, selected [][]daemon.MessagePart, userOnly bool) {
	unit := "messages"
	if userOnly {
		unit = "user messages"
	}
	fmt.Fprintf(w, "%s  %s  %s\n",
		queryDim.Render("session "+displaySessionID(c.id)),
		queryTitleStyle.Render(ansi.Truncate(c.title, 60, "…")),
		queryDim.Render(fmt.Sprintf("(%d %s, showing %d)", total, unit, len(selected))))
	if len(selected) == 0 {
		fmt.Fprintln(w, queryDim.Render("(no messages)"))
		return
	}
	const roleW = 9 // "assistant"
	for _, g := range selected {
		role := g[0].Role
		if role == "" {
			role = "?"
		}
		prefix := padCell(styledRole(role), roleW) + "  " + queryDim.Render(queryTimeHM(g[0].TimeCreated)) + "  "
		indent := strings.Repeat(" ", lipgloss.Width(prefix))
		fmt.Fprintln(w, prefix+strings.ReplaceAll(joinQueryParts(g), "\n", "\n"+indent))
	}
}

// queryProjectRowStatus 聚合 project 下全部 root session 的 RowStatus，
// 取优先级最高（数值最小）者作为 project 行状态；无 session 时返回空。
func queryProjectRowStatus(p daemon.SidebarProject) daemon.SessionStatus {
	var best daemon.SessionStatus
	bestP := 1 << 30
	for _, s := range p.Sessions {
		if pr, ok := queryStatusPriority[s.RowStatus]; ok && pr < bestP {
			bestP = pr
			best = s.RowStatus
		}
	}
	return best
}

// styledStatus 渲染"emoji 状态点 + 彩色状态词"单元，列宽固定为
// glyph(2) + 空格 + 最长状态词 PERMISSION(10)。
func styledStatus(st daemon.SessionStatus) string {
	color := queryStatusColor[st]
	if color == "" {
		color = "#565f89"
	}
	return st.Glyph() + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(string(st))
}

func styledRole(role string) string {
	color := queryRoleColor[strings.ToLower(role)]
	if color == "" {
		color = "#565f89"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(role)
}

// joinQueryParts 拼接同一消息多个 part 的文本（跳过空文本），用换行分隔。
func joinQueryParts(g []daemon.MessagePart) string {
	texts := make([]string, 0, len(g))
	for _, p := range g {
		if strings.TrimSpace(p.Text) != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// queryStateTime 取状态最近一次变动时间：事件优先，回退 DB 同步时间。
func queryStateTime(st daemon.SessionState) time.Time {
	t := st.LastEventAt
	if st.LastSyncAt.After(t) {
		t = st.LastSyncAt
	}
	return t
}

func queryStateTimeMs(st daemon.SessionState) int64 {
	return queryStateTime(st).UnixMilli()
}

// queryProjectLabel 优先用 project 显示名，查不到时回退 projectID。
func queryProjectLabel(projectID string, names map[string]string) string {
	if n := names[projectID]; n != "" {
		return n
	}
	return projectID
}

// relAgeMs 把 unix 毫秒时间戳渲染为相对年龄（30s / 5m / 3h / 2d）。
func relAgeMs(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Since(time.UnixMilli(ms)).Round(time.Second)
	switch {
	case d < 0:
		return "-" // 时钟偏移的防御
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// queryTimeHM 渲染 HH:MM；时间戳为 0（未知）时渲染 "-"。
func queryTimeHM(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("15:04")
}

// padCell 按显示宽度（ANSI 感知）右侧补空格对齐到固定列宽。
func padCell(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// printQueryUsage 输出 query 子命令帮助。
func printQueryUsage(w io.Writer) {
	fmt.Fprintf(w, `Usage: octl query <method> [flags]

Methods:
  snaps                   所有 session 实时状态
  sessions                project/session 树（含 root 聚合状态）
  messages <sessionId>    对话内容；sessionId 模糊匹配（不区分大小写子串，唯一命中才执行）
  daily                   时间窗口内的活动聚合（新增/活跃/归档/僵尸/卡住 + 摘要素材）

Flags:
  --json            输出完整 JSON（stdout 纯 JSON，供 jq 等脚本消费）
  --nums <spec>     仅 messages：消息选择器（默认 0）
  --user-only       仅 messages：只保留用户消息（主题骨架），先过滤再按 --nums 选择
  --date <day>      仅 daily：单个自然日（本地时区），如 2026-09-04
  --from <time>     仅 daily：窗口起点，如 2026-09-01 或 2026-09-01T14:00
  --to <time>       仅 daily：窗口终点（不含），缺省为当前时刻
  --socket <path>   daemon socket 路径（默认 ~/.local/share/opencode/octl.sock）
  --timeout <sec>   响应超时秒数（默认 5）

--nums 选择器语法：
  单值    1=第一条，0=最后一条，-1=上一条，-2=再上一条
  范围    3-5、4--2（第 4 条到 -2 条）、-2-6（降序自动交换为升序）
  列表    1,3,-1     全部    all
  超界钳制：正数溢出取最后一条，负数溢出取第一条

daily 窗口语义：
  缺省 = 今天（本地时区自然日）；--date 与 --from/--to 互斥；
  只给 --to 不行（缺 --from）；--from 可单独使用（--to=now）
  时间格式：2026-09-04 / 2026-09-04T14:00 / 2026-09-04 14:00（本地时区）

示例：
  octl query snaps
  octl query snaps --json | jq '.states[] | select(.status=="BUSY")'
  octl query sessions
  octl query messages 5v1n --nums all
  octl query messages ses_5V1N --nums 1,3-5,-1
  octl query messages ses_5V1N --user-only --nums all   # 用户消息骨架（主题脉络）
  octl query daily                        # 今天
  octl query daily --date 2026-09-04      # 指定某天
  octl query daily --from 2026-08-31      # 8-31 至今

退出码：0 成功；1 运行错误（daemon 未运行/超时/查询失败）；2 用法错误
`)
}
