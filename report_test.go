// report_test.go（根包）：octl report 子命令的帮助回归与用法错误。
package main

import (
	"strings"
	"testing"
)

// TestPrintReportUsage 帮助必须覆盖窗口 flags、目录覆盖与自动落盘说明。
func TestPrintReportUsage(t *testing.T) {
	var b strings.Builder
	printReportUsage(&b)
	out := b.String()
	for _, want := range []string{
		"octl report — 生成日报底片（机械事实层）并落盘",
		"--date <day>      单个自然日",
		"--from <time>     窗口起点（--to 缺省为当前时刻）",
		"--dir <path>      输出目录（daemon 侧展开",
		"octl report --date 2026-09-04        # 补写 9 月 4 日终版",
		"daemon 也会自动落盘",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report 帮助缺少 %q:\n%s", want, out)
		}
	}
}

// TestRunReport_UsageErrors 用法错误在连接 daemon 前拦截（退出码 2）。
func TestRunReport_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"date与from互斥", []string{"--date", "2026-09-04", "--from", "2026-09-01"}},
		{"只给to", []string{"--to", "2026-09-05"}},
		{"date格式错误", []string{"--date", "2026/09/04"}},
		{"多余位置参数", []string{"extra"}},
		{"timeout非法", []string{"--timeout", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := runReport(tt.args); code != queryExitUsage {
				t.Errorf("runReport(%v) = %d, want %d", tt.args, code, queryExitUsage)
			}
		})
	}
}
