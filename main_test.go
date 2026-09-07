// main_test.go 锁定 help 文本的回归测试：新增子命令或方法时必须同步
// 主帮助与 query 帮助，漏改点位在这里直接失败（含 flag 列表完整性）。
package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// newMainFlagSet 构建与 main() 相同业务 flag 的独立 FlagSet——不碰全局
// flag.CommandLine（测试二进制里它被 testing 框架注入 -test.* flag）。
func newMainFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("octl", flag.ContinueOnError)
	fs.Bool("daemon", false, "Run the octl daemon service (foreground)")
	fs.Bool("tui", false, "Run the TUI (default)")
	fs.String("socket", "", "Path to the octl daemon Unix socket")
	fs.Int("refresh-time", 5, "Dashboard auto-refresh interval in seconds (0 = disable)")
	fs.Bool("version", false, "Print version and exit")
	return fs
}

// TestMainUsage_ListsAllSubcommands 断言主帮助覆盖全部子命令与
// query 方法枚举（含 daily），防止"子命令存在但帮助没写"的漂移。
func TestMainUsage_ListsAllSubcommands(t *testing.T) {
	var b bytes.Buffer
	printMainUsage(&b, newMainFlagSet())
	out := b.String()

	for _, want := range []string{
		"octl --daemon [flags]", // daemon 模式
		"octl plugins [flags]",  // plugins 子命令
		"octl delete [flags]",   // 动作子命令
		"octl create [flags]",
		"octl fork [flags]",
		"octl send [flags]",
		"octl purge [flags]", // 影子库真删除出口
		// query 方法枚举与 query 子命令说明各出现一次 daily
		"octl query [flags]     One-shot daemon query: snaps | sessions | messages <id> | daily",
		"Methods: snaps | sessions | messages <sessionId> | daily",
		"octl report [flags]    Write the daily raw digest (daily factual base)",
		"octl report [--date <day> | --from <t> [--to <t>]] [--dir <path>]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("主帮助缺少 %q:\n%s", want, out)
		}
	}
}

// TestMainUsage_PrintsFlagDefaults 断言 Flags 段完整输出注入 FlagSet 的
// 业务 flag（PrintDefaults 走 printMainUsage 的 out，不串台全局状态）。
func TestMainUsage_PrintsFlagDefaults(t *testing.T) {
	var b bytes.Buffer
	printMainUsage(&b, newMainFlagSet())
	out := b.String()

	for _, want := range []string{"-daemon", "-tui", "-socket", "-refresh-time", "-version"} {
		if !strings.Contains(out, want) {
			t.Errorf("主帮助 Flags 段缺少 %q:\n%s", want, out)
		}
	}
	// testing 注入的 flag 不应出现在输出里。
	if strings.Contains(out, "-test.") {
		t.Errorf("主帮助混入了 testing 框架 flag:\n%s", out)
	}
}
