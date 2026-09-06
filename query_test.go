package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/daemon"
)

// TestParseNums 覆盖 --nums 选择器的全部语法形态与钳制规则。
func TestParseNums(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		total   int
		want    []int
		wantErr bool
	}{
		{"默认最后一条", "0", 10, []int{10}, false},
		{"第一条", "1", 10, []int{1}, false},
		{"上一条", "-1", 10, []int{9}, false},
		{"再上一条", "-2", 10, []int{8}, false},
		{"列表", "1,3,5", 10, []int{1, 3, 5}, false},
		{"列表混合负数", "1,3,-1", 10, []int{1, 3, 9}, false},
		{"列表去重", "1,1,1", 5, []int{1}, false},
		{"列表去重跨写法", "0,-1,9,10", 10, []int{9, 10}, false},
		{"范围升序", "3-5", 10, []int{3, 4, 5}, false},
		{"范围到末尾", "4--2", 10, []int{4, 5, 6, 7, 8}, false},
		{"降序自动交换", "-2-6", 10, []int{6, 7, 8}, false},
		{"范围含0终点", "3-0", 10, []int{3, 4, 5, 6, 7, 8, 9, 10}, false},
		{"单点范围", "0-0", 10, []int{10}, false},
		{"负范围", "-3--1", 10, []int{7, 8, 9}, false},
		{"正溢出钳到最后", "99", 5, []int{5}, false},
		{"负溢出钳到第一", "-99", 5, []int{1}, false},
		{"范围双端溢出", "-99-99", 5, []int{1, 2, 3, 4, 5}, false},
		{"all", "all", 3, []int{1, 2, 3}, false},
		{"all 与列表混合", "1,all", 2, []int{1, 2}, false},
		{"带空格", " 1 , 3 ", 10, []int{1, 3}, false},
		{"无消息", "0", 0, nil, false},
		{"all 无消息", "all", 0, nil, false},
		{"非法元素", "1,x", 10, nil, true},
		{"双前缀负号", "--5", 10, nil, true},
		{"空字符串", "", 10, nil, true},
		{"纯空格", "  ", 10, nil, true},
		{"字母范围", "a-b", 10, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNums(tt.spec, tt.total)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseNums(%q) expected error, got %v", tt.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNums(%q): %v", tt.spec, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNums(%q, %d) = %v, want %v", tt.spec, tt.total, got, tt.want)
			}
		})
	}
}

// TestGroupMessageParts 同一 MessageID 的连续 part 应合并为一组。
func TestGroupMessageParts(t *testing.T) {
	parts := []daemon.MessagePart{
		{MessageID: "m1", Role: "user", Text: "a"},
		{MessageID: "m1", Role: "user", Text: "b"},
		{MessageID: "m2", Role: "assistant", Text: "c"},
		{MessageID: "m3", Role: "assistant", Text: "d"},
		{MessageID: "m3", Role: "assistant", Text: "e"},
		{MessageID: "m3", Role: "assistant", Text: "f"},
	}
	groups := groupMessageParts(parts)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	wantLens := []int{2, 1, 3}
	for i, want := range wantLens {
		if len(groups[i]) != want {
			t.Errorf("group %d len = %d, want %d", i, len(groups[i]), want)
		}
	}
	if groups[0][0].Text != "a" || groups[2][2].Text != "f" {
		t.Errorf("group content mismatch: %+v", groups)
	}
}

func TestGroupMessageParts_Empty(t *testing.T) {
	if got := groupMessageParts(nil); len(got) != 0 {
		t.Errorf("expected 0 groups, got %d", len(got))
	}
}

// TestResolveQuerySession 模糊匹配：不区分大小写子串、唯一命中、
// 零命中与多命中歧义。
func TestResolveQuerySession(t *testing.T) {
	cands := []queryCandidate{
		{id: "ses_5V1Nkq", title: "Fix daemon sync"},
		{id: "ses_5V1DIFF", title: "Add query CLI"},
		{id: "ses_ABCDEF", title: "Other"},
	}
	tests := []struct {
		name      string
		input     string
		wantID    string
		wantErr   bool
		errSubstr string
	}{
		{"大小写不敏感", "5V1N", "ses_5V1Nkq", false, ""},
		{"带前缀输入", "ses_5v1diff", "ses_5V1DIFF", false, ""},
		{"完整ID", "SES_ABCDEF", "ses_ABCDEF", false, ""},
		{"零命中", "zzz", "", true, "找不到"},
		{"多命中", "5v1", "", true, "2 个"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveQuerySession(cands, tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveQuerySession(%q): %v", tt.input, err)
			}
			if got.id != tt.wantID {
				t.Errorf("id = %q, want %q", got.id, tt.wantID)
			}
		})
	}
}

func TestDisplaySessionID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ses_5V1NAbc", "5v1nabc"},
		{"abc", "abc"},
		{" SES_XY ", "xy"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := displaySessionID(tt.in); got != tt.want {
			t.Errorf("displaySessionID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestRelAgeMs 相对年龄渲染（以固定偏移构造，Round 到秒后稳定）。
func TestRelAgeMs(t *testing.T) {
	now := time.Now().UnixMilli()
	if got := relAgeMs(0); got != "-" {
		t.Errorf("relAgeMs(0) = %q, want %q", got, "-")
	}
	if got := relAgeMs(-1000); got != "-" {
		t.Errorf("relAgeMs(未来时间) = %q, want %q", got, "-")
	}
	tests := []struct {
		offMs int64
		want  string
	}{
		{30_000, "30s"},        // 30s
		{5 * 60_000, "5m"},     // 5m
		{3 * 3_600_000, "3h"},  // 3h
		{2 * 86_400_000, "2d"}, // 2d
	}
	for _, tt := range tests {
		if got := relAgeMs(now - tt.offMs); got != tt.want {
			t.Errorf("relAgeMs(offset %dms) = %q, want %q", tt.offMs, got, tt.want)
		}
	}
}

// TestPadCell_ANSIAware 补齐宽度必须按 ANSI 感知的显示宽度计算，
// 而不是字节或 rune 数。
func TestPadCell_ANSIAware(t *testing.T) {
	styled := "\x1b[38;2;126;162;247mBUSY\x1b[0m"
	got := padCell(styled, 8)
	want := styled + "    "
	if got != want {
		t.Errorf("padCell = %q, want %q", got, want)
	}
	// CJK 宽度：中文标题占 2 列。
	if got := padCell("你好", 6); got != "你好  " {
		t.Errorf("padCell CJK = %q", got)
	}
}

// TestReorderQueryArgs method 在前、flags 在后的参数序列应被正确分离重组。
func TestReorderQueryArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		flgs []string
		pos  []string
	}{
		{"flag在方法后", []string{"snaps", "--nums", "1"}, []string{"--nums", "1"}, []string{"snaps"}},
		{"混合顺序", []string{"messages", "5v1n", "--nums", "all", "--json"}, []string{"--nums", "all", "--json"}, []string{"messages", "5v1n"}},
		{"flag在前", []string{"--json", "snaps"}, []string{"--json"}, []string{"snaps"}},
		{"等号形式", []string{"snaps", "--nums=1,3"}, []string{"--nums=1,3"}, []string{"snaps"}},
		{"单横线", []string{"snaps", "-json"}, []string{"-json"}, []string{"snaps"}},
		{"负数值", []string{"messages", "x", "--nums", "-1"}, []string{"--nums", "-1"}, []string{"messages", "x"}},
		{"终止符", []string{"messages", "--", "-weird"}, []string{}, []string{"messages", "-weird"}},
		{"未知flag不吞值", []string{"snaps", "--bogus", "1"}, []string{"--bogus"}, []string{"snaps", "1"}},
		{"裸横线是位置参数", []string{"messages", "-"}, []string{}, []string{"messages", "-"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flgs, pos := reorderQueryArgs(tt.in)
			if flgs == nil {
				flgs = []string{}
			}
			if pos == nil {
				pos = []string{}
			}
			if !reflect.DeepEqual(flgs, tt.flgs) {
				t.Errorf("flags = %v, want %v", flgs, tt.flgs)
			}
			if !reflect.DeepEqual(pos, tt.pos) {
				t.Errorf("positional = %v, want %v", pos, tt.pos)
			}
		})
	}
}

// TestRunQuery_UsageErrors 参数校验应在连接 daemon 之前完成，
// 不需要 socket 即可验证退出码。
func TestRunQuery_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"缺少方法", nil},
		{"未知方法", []string{"bogus"}},
		{"messages缺sessionId", []string{"messages"}},
		{"nums误用于snaps", []string{"snaps", "--nums", "1"}},
		{"nums等号误用于sessions", []string{"sessions", "--nums=all"}},
		{"useronly误用于snaps", []string{"snaps", "--user-only"}},
		{"useronly误用于daily", []string{"daily", "--user-only"}},
		{"timeout非法", []string{"snaps", "--timeout", "0"}},
		{"参数过多snaps", []string{"snaps", "extra"}},
		{"参数过多messages", []string{"messages", "a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := runQuery(tt.args); code != queryExitUsage {
				t.Errorf("runQuery(%v) = %d, want %d", tt.args, code, queryExitUsage)
			}
		})
	}
}

// TestRunQuery_DailyUsageErrors daily 方法的参数校验同样发生在连接前。
func TestRunQuery_DailyUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"date与from互斥", []string{"daily", "--date", "2026-09-04", "--from", "2026-09-01"}},
		{"date与to互斥", []string{"daily", "--date", "2026-09-04", "--to", "2026-09-05"}},
		{"只给to", []string{"daily", "--to", "2026-09-05"}},
		{"date格式错误", []string{"daily", "--date", "2026/09/04"}},
		{"from格式错误", []string{"daily", "--from", "yesterday"}},
		{"from晚于to", []string{"daily", "--from", "2026-09-05", "--to", "2026-09-01"}},
		{"from等于to", []string{"daily", "--from", "2026-09-05T10:00", "--to", "2026-09-05T10:00"}},
		{"date误用于snaps", []string{"snaps", "--date", "2026-09-04"}},
		{"from误用于messages", []string{"messages", "x", "--from", "2026-09-04"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := runQuery(tt.args); code != queryExitUsage {
				t.Errorf("runQuery(%v) = %d, want %d", tt.args, code, queryExitUsage)
			}
		})
	}
}

// TestParseDailyWindow 覆盖窗口解析的默认值、格式与校验规则。
func TestParseDailyWindow(t *testing.T) {
	now := time.Date(2026, 9, 4, 15, 30, 0, 0, time.Local)

	tests := []struct {
		name           string
		date, from, to string
		wantFrom       time.Time
		wantTo         time.Time
		wantErr        bool
	}{
		{
			name: "缺省为今天", date: "", from: "", to: "",
			wantFrom: time.Date(2026, 9, 4, 0, 0, 0, 0, time.Local),
			wantTo:   time.Date(2026, 9, 5, 0, 0, 0, 0, time.Local),
		},
		{
			name: "单日date", date: "2026-09-01", from: "", to: "",
			wantFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local),
			wantTo:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.Local),
		},
		{
			name: "date带时间被拒绝", date: "2026-09-01T10:00", from: "", to: "",
			wantErr: true,
		},
		{
			name: "from+to完整", date: "", from: "2026-09-01", to: "2026-09-07",
			wantFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local),
			wantTo:   time.Date(2026, 9, 7, 0, 0, 0, 0, time.Local),
		},
		{
			name: "from带T时间", date: "", from: "2026-09-01T14:30", to: "2026-09-02",
			wantFrom: time.Date(2026, 9, 1, 14, 30, 0, 0, time.Local),
			wantTo:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.Local),
		},
		{
			name: "from带空格时间", date: "", from: "2026-09-01 14:30", to: "2026-09-02",
			wantFrom: time.Date(2026, 9, 1, 14, 30, 0, 0, time.Local),
			wantTo:   time.Date(2026, 9, 2, 0, 0, 0, 0, time.Local),
		},
		{
			name: "from缺省to为now", date: "", from: "2026-09-04", to: "",
			wantFrom: time.Date(2026, 9, 4, 0, 0, 0, 0, time.Local),
			wantTo:   now,
		},
		{name: "date与from互斥", date: "2026-09-04", from: "2026-09-01", to: "", wantErr: true},
		{name: "只给to", date: "", from: "", to: "2026-09-05", wantErr: true},
		{name: "from不早于to", date: "", from: "2026-09-05", to: "2026-09-01", wantErr: true},
		{name: "from等于to", date: "", from: "2026-09-05", to: "2026-09-05", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, err := parseDailyWindow(tt.date, tt.from, tt.to, now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got window [%d, %d)", from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDailyWindow: %v", err)
			}
			if from != tt.wantFrom.UnixMilli() {
				t.Errorf("from = %d, want %d", from, tt.wantFrom.UnixMilli())
			}
			if to != tt.wantTo.UnixMilli() {
				t.Errorf("to = %d, want %d", to, tt.wantTo.UnixMilli())
			}
		})
	}
}

// TestReorderQueryArgs_DailyFlags 验证 daily 的值型 flag 会被正确吞并。
func TestReorderQueryArgs_DailyFlags(t *testing.T) {
	flgs, pos := reorderQueryArgs([]string{"daily", "--date", "2026-09-04", "--json"})
	if !reflect.DeepEqual(flgs, []string{"--date", "2026-09-04", "--json"}) {
		t.Errorf("flags = %v", flgs)
	}
	if !reflect.DeepEqual(pos, []string{"daily"}) {
		t.Errorf("positional = %v", pos)
	}

	flgs, pos = reorderQueryArgs([]string{"daily", "extra", "--from=2026-09-01", "--to", "2026-09-02"})
	if !reflect.DeepEqual(flgs, []string{"--from=2026-09-01", "--to", "2026-09-02"}) {
		t.Errorf("flags = %v", flgs)
	}
	if !reflect.DeepEqual(pos, []string{"daily", "extra"}) {
		t.Errorf("positional = %v", pos)
	}
}

// TestFilterUserGroups --user-only 的过滤语义：保留 user 组、剔除
// assistant 组、序号在过滤后序列上重排（下游 --nums 语义随之改变）。
func TestFilterUserGroups(t *testing.T) {
	mk := func(id, role string) [][]daemon.MessagePart {
		return [][]daemon.MessagePart{{{MessageID: id, Role: role}}}
	}
	groups := [][]daemon.MessagePart{
		{{MessageID: "m1", Role: "user"}},
		{{MessageID: "m2", Role: "assistant"}},
		{{MessageID: "m3", Role: "user"}},
		{{MessageID: "m4", Role: "assistant"}},
		{{MessageID: "m5", Role: "user"}},
	}
	got := filterUserGroups(groups)
	if len(got) != 3 {
		t.Fatalf("filterUserGroups: got %d groups, want 3", len(got))
	}
	// 序号重排：过滤后第 2 条应是原 m3。
	if got[1][0].MessageID != "m3" {
		t.Errorf("filtered[1] = %s, want m3（序号应基于过滤后序列）", got[1][0].MessageID)
	}

	if got := filterUserGroups(mk("m1", "assistant")); len(got) != 0 {
		t.Errorf("all-assistant: got %d groups, want 0", len(got))
	}
	if got := filterUserGroups(nil); len(got) != 0 {
		t.Errorf("nil input: got %d groups, want 0", len(got))
	}
	// 空 role 不算 user（防御：不应把缺 role 的组误当骨架）。
	if got := filterUserGroups(mk("m1", "")); len(got) != 0 {
		t.Errorf("empty-role: got %d groups, want 0", len(got))
	}
	// 空 group 不 panic。
	if got := filterUserGroups([][]daemon.MessagePart{{}}); len(got) != 0 {
		t.Errorf("empty-group: got %d groups, want 0", len(got))
	}
}

// TestRenderMessages_UserOnly 渲染 header 在 user-only 模式下标注单位，
// 避免 "N messages" 被误读为全量消息数。
func TestRenderMessages_UserOnly(t *testing.T) {
	var plain, filtered strings.Builder
	c := queryCandidate{id: "ses_T1", title: "test"}
	groups := [][]daemon.MessagePart{
		{{MessageID: "m1", Role: "user", Text: "hello"}},
		{{MessageID: "m2", Role: "assistant", Text: "hi"}},
	}
	renderMessages(&plain, c, 2, groups, false)
	renderMessages(&filtered, c, 1, groups[:1], true)
	if !strings.Contains(plain.String(), "(2 messages, showing 2)") {
		t.Errorf("plain header missing total: %q", plain.String())
	}
	if !strings.Contains(filtered.String(), "(1 user messages, showing 1)") {
		t.Errorf("user-only header missing label: %q", filtered.String())
	}
}

// TestPrintQueryUsage_UserOnly 帮助必须覆盖 --user-only 的 flag 行与
// 骨架拉取示例。
func TestPrintQueryUsage_UserOnly(t *testing.T) {
	var b strings.Builder
	printQueryUsage(&b)
	out := b.String()
	for _, want := range []string{
		"--user-only       仅 messages：只保留用户消息（主题骨架）",
		"octl query messages ses_5V1N --user-only --nums all",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("query 帮助缺少 %q:\n%s", want, out)
		}
	}
}

// TestPrintQueryUsage_Daily query 帮助必须完整覆盖 daily：方法行、三个
// 窗口 flag、窗口语义段与示例，防止只加方法不更新帮助的回归。
func TestPrintQueryUsage_Daily(t *testing.T) {
	var b strings.Builder
	printQueryUsage(&b)
	out := b.String()

	for _, want := range []string{
		"daily                   时间窗口内的活动聚合",
		"--date <day>      仅 daily：单个自然日",
		"--from <time>     仅 daily：窗口起点",
		"--to <time>       仅 daily：窗口终点（不含），缺省为当前时刻",
		"daily 窗口语义：",
		"--date 与 --from/--to 互斥",
		"时间格式：2026-09-04 / 2026-09-04T14:00 / 2026-09-04 14:00（本地时区）",
		"octl query daily                        # 今天",
		"octl query daily --date 2026-09-04      # 指定某天",
		"octl query daily --from 2026-08-31      # 8-31 至今",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("query 帮助缺少 %q:\n%s", want, out)
		}
	}
}

// TestRenderDaily_Smoke 验证渲染主干：project 分节、摘录行与僵尸节。
func TestRenderDaily_Smoke(t *testing.T) {
	d := &daemon.DailyDigest{
		From:        1800000000000,
		To:          1800086400000,
		TotalActive: 2,
		Excerpted:   1,
		Projects: []daemon.DailyProject{{
			ProjectID:      "proj-a",
			Name:           "Alpha",
			Worktree:       "/a",
			SessionCostSum: 1.5,
			NewSessions:    []daemon.DailySessionRef{{SessionID: "s2", Title: "T2", ProjectID: "proj-a", TimeCreated: 1800003600000}},
			ActiveSessions: []daemon.DailySession{
				{
					SessionID: "s1", Title: "T1", MsgCount: 3,
					FirstActivity: 1800000000000, LastActivity: 1800040000000,
					HasExcerpt:           true,
					FirstUserExcerpt:     "帮我修\n浮层",
					LastAssistantExcerpt: "已修复",
				},
				{SessionID: "s2", Title: "T2", MsgCount: 1, FirstActivity: 1800003600000, LastActivity: 1800003600000},
			},
		}},
		Zombies: []daemon.DailySessionRef{{SessionID: "s3", Title: "T3", LastActivity: 1799000000000}},
	}

	var b strings.Builder
	renderDaily(&b, d)
	out := b.String()
	for _, want := range []string{
		"daily digest",
		"active 2 · excerpted 1",
		"Alpha",
		"new 1 · active 2 · archived 0 · cost $1.50",
		"ACT",
		"  3msg",
		"首条: 帮我修 ⏎ 浮层",
		"进展: 已修复",
		"NEW",
		"ZOMBIE (1)",
		"idle",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render output missing %q:\n%s", want, out)
		}
	}
}
