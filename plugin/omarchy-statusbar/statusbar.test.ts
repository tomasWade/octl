// @ts-check
// octl.sessions omarchy bar-widget 纯逻辑测试（bun:test）。
// 被测对象是 QML/bun 双兼容的 statusbar.js：QML JS 资源不支持 export，
// 经 CommonJS 互操作（module.exports 守卫）导入。
import { describe, test, expect } from "bun:test";
import Statusbar from "../../internal/plugins/templates/omarchy-statusbar/statusbar.js";

const {
  STATUS_GROUPS,
  shQuote,
  collectSessions,
  groupCounts,
  truncateTitle,
  groupTooltip,
  collectFavorites,
  favoriteRows,
  makeTmuxSessionName,
  classifyJump,
  buildJumpScript,
} = Statusbar as any;

function sess(id: string, over: Record<string, any> = {}): Record<string, any> {
  return Object.assign(
    { sessionId: id, title: "t-" + id, status: "IDLE", pid: 0, tmuxPane: "", tmuxSession: "", directory: "/tmp" },
    over,
  );
}

describe("STATUS_GROUPS", () => {
  test("固定顺序 ERROR→PERMISSION→RETRY→BUSY，图标一一对应", () => {
    expect(STATUS_GROUPS.map((g: any[]) => g[0])).toEqual(["ERROR", "PERMISSION", "RETRY", "BUSY"]);
    expect(STATUS_GROUPS.map((g: any[]) => g[1])).toEqual(["🔴", "🟡", "🟠", "🔵"]);
  });
});

describe("shQuote", () => {
  test("单引号包裹，防 sh 展开", () => {
    expect(shQuote("$29")).toBe("'$29'");
    expect(shQuote("%5")).toBe("'%5'");
    expect(shQuote("")).toBe("''");
  });
  test("内嵌单引号转义，控制字符替换为空格", () => {
    expect(shQuote("a'b")).toBe("'a'\\''b'");
    expect(shQuote("line1\nline2")).toBe("'line1 line2'");
  });
});

describe("collectSessions", () => {
  test("摊平 projects[].sessions[]，按 sessionId 去重", () => {
    const a = sess("ses_a");
    const aDup = sess("ses_a");
    const b = sess("ses_b");
    const view = {
      projects: [
        { projectId: "p1", sessions: [a, aDup] },
        { projectId: "p2", sessions: [b] },
        { projectId: "p3", sessions: [] },
      ],
    };
    const out = collectSessions(view);
    expect(out.map((s: any) => s.sessionId)).toEqual(["ses_a", "ses_b"]);
  });
  test("空 view / 缺 sessionId 安全", () => {
    expect(collectSessions({})).toEqual([]);
    expect(collectSessions(null)).toEqual([]);
    expect(collectSessions({ projects: [{ sessions: [{ status: "BUSY" }, null] }] })).toEqual([]);
  });
});

describe("groupCounts", () => {
  test("固定顺序、仅非零、忽略 IDLE/UNKNOWN/ARCHIVED", () => {
    const out = groupCounts([
      sess("a", { status: "BUSY" }),
      sess("b", { status: "BUSY" }),
      sess("c", { status: "ERROR" }),
      sess("d", { status: "IDLE" }),
      sess("e", { status: "UNKNOWN" }),
      sess("f", { status: "ARCHIVED" }),
    ]);
    expect(out).toEqual([
      { key: "ERROR", icon: "🔴", n: 1 },
      { key: "BUSY", icon: "🔵", n: 2 },
    ]);
  });
  test("四类齐全时保持定序", () => {
    const out = groupCounts([
      sess("b", { status: "BUSY" }),
      sess("r", { status: "RETRY" }),
      sess("p", { status: "PERMISSION" }),
      sess("e", { status: "ERROR" }),
    ]);
    expect(out.map((g: any) => g.key)).toEqual(["ERROR", "PERMISSION", "RETRY", "BUSY"]);
    expect(out.every((g: any) => g.n === 1)).toBe(true);
  });
  test("空输入 / 全 IDLE → 空数组", () => {
    expect(groupCounts([])).toEqual([]);
    expect(groupCounts([sess("a"), sess("b")])).toEqual([]);
  });
});

describe("truncateTitle", () => {
  test("短标题原样返回", () => {
    expect(truncateTitle("short", 40)).toBe("short");
  });
  test("超限截断加省略号，总长不超过限制", () => {
    const long = "x".repeat(50);
    const out = truncateTitle(long, 40);
    expect(out.length).toBe(40);
    expect(out.endsWith("…")).toBe(true);
  });
  test("缺省 40 字符", () => {
    expect(truncateTitle("y".repeat(41)).length).toBe(40);
    expect(truncateTitle(null)).toBe("");
  });
});

describe("groupTooltip", () => {
  test("按状态分组、组序固定、标题截断", () => {
    const out = groupTooltip([
      sess("ses_a", { title: "A".repeat(60), status: "BUSY" }),
      sess("ses_b", { status: "ERROR" }),
    ]);
    expect(out.hidden).toBe(0);
    expect(out.groups.map((g: any) => g.key)).toEqual(["ERROR", "BUSY"]);
    expect(out.groups[0].items[0].sessionId).toBe("ses_b");
    expect(out.groups[1].items[0].title.length).toBe(40);
    // items 保留完整 session 引用供跳转使用
    expect(out.groups[1].items[0].session.sessionId).toBe("ses_a");
  });
  test("超过 8 条折叠，hidden 计余量", () => {
    const list = [];
    for (let i = 0; i < 10; i++) list.push(sess("s" + i, { status: "BUSY" }));
    const out = groupTooltip(list);
    expect(out.groups[0].items.length).toBe(8);
    expect(out.hidden).toBe(2);
    expect(out.groups[0].items[0].sessionId).toBe("s0");
    expect(out.groups[0].items[7].sessionId).toBe("s7");
  });
  test("折叠按分组顺序消耗名额（ERROR 优先于 BUSY）", () => {
    const list = [
      ...Array.from({ length: 6 }, (_, i) => sess("e" + i, { status: "ERROR" })),
      ...Array.from({ length: 6 }, (_, i) => sess("b" + i, { status: "BUSY" })),
    ];
    const out = groupTooltip(list);
    expect(out.groups[0].items.length).toBe(6); // ERROR 全保留
    expect(out.groups[1].items.length).toBe(2); // BUSY 只剩 2 个名额
    expect(out.hidden).toBe(4);
  });
  test("自定义 maxItems 与空输入", () => {
    const out = groupTooltip([sess("a", { status: "BUSY" }), sess("b", { status: "BUSY" })], 1);
    expect(out.groups[0].items.length).toBe(1);
    expect(out.hidden).toBe(1);
    expect(groupTooltip([])).toEqual({ groups: [], hidden: 0 });
  });
});

describe("collectFavorites", () => {
  test("提取 view.favorites，保持插入顺序，缺 sessionId 过滤", () => {
    const a = sess("ses_a");
    const b = sess("ses_b", { status: "BUSY", pid: 123, tmuxPane: "%5", tmuxSession: "$29" });
    const out = collectFavorites({ favorites: [a, b] });
    expect(out).toEqual([a, b]);
    expect(out[1].tmuxSession).toBe("$29");
  });
  test("按 sessionId 去重（first-win）", () => {
    const a = sess("ses_a");
    const aDup = sess("ses_a");
    const out = collectFavorites({ favorites: [a, aDup] });
    expect(out).toHaveLength(1);
    expect(out[0]).toBe(a);
  });
  test("空 view / 缺字段 / null 元素安全", () => {
    expect(collectFavorites({})).toEqual([]);
    expect(collectFavorites(null)).toEqual([]);
    expect(collectFavorites({ favorites: [null, sess("ses_x"), {}] }).map((s: any) => s.sessionId)).toEqual(["ses_x"]);
  });
});

describe("favoriteRows", () => {
  test("映射行结构：标题截断 / 保留完整 session 引用 / 不带状态图标", () => {
    const busy = sess("ses_a", { title: "A".repeat(60), status: "BUSY", pid: 123, tmuxPane: "%5", tmuxSession: "$29" });
    const idle = sess("ses_b");
    const out = favoriteRows([busy, idle]);
    expect(out).toHaveLength(2);
    expect(out[0].sessionId).toBe("ses_a");
    expect(out[0].title.length).toBe(40);
    expect(out[0].title.endsWith("…")).toBe(true);
    // session 完整引用供 buildJumpScript 跳转使用；活跃态也不带状态图标
    expect(out[0].session).toBe(busy);
    expect(out[0]).not.toHaveProperty("icon");
    expect(out[1].session).toBe(idle);
  });
  test("不折叠：全量输出", () => {
    const list = Array.from({ length: 12 }, (_, i) => sess("f" + i));
    expect(favoriteRows(list)).toHaveLength(12);
  });
  test("空输入安全", () => {
    expect(favoriteRows([])).toEqual([]);
    expect(favoriteRows(null)).toEqual([]);
  });
});

describe("makeTmuxSessionName", () => {
  test("清理特殊字符、截 15 字、回退链", () => {
    expect(makeTmuxSessionName("Fix: daemon sync #12!!", "")).toBe("Fixdaemonsync12");
    expect(makeTmuxSessionName("标题！特殊@字符", "")).toBe("标题特殊字符");
    expect(makeTmuxSessionName("0123456789abcdef", "")).toBe("0123456789abcde");
    expect(makeTmuxSessionName("", "ses_xyz")).toBe("sesxyz");
    // 与 sidebar 同规则：title 清理后为空不再回退 sessionId（跨表面命名一致）
    expect(makeTmuxSessionName("///", "ses_x")).toBe("unknown");
    expect(makeTmuxSessionName("", "")).toBe("unknown");
  });
});

describe("classifyJump", () => {
  test("四分类", () => {
    expect(classifyJump(sess("a", { tmuxPane: "%5", tmuxSession: "$29", pid: 123 }))).toBe("attached");
    expect(classifyJump(sess("a", { tmuxPane: "%5", tmuxSession: "$29", pid: 0 }))).toBe("detached");
    expect(classifyJump(sess("a", { pid: 4242 }))).toBe("bare");
    expect(classifyJump(sess("a", {}))).toBe("dead");
    expect(classifyJump(null)).toBe("dead");
  });
});

describe("buildJumpScript", () => {
  test("空 session 返回空串", () => {
    expect(buildJumpScript(null)).toBe("");
    expect(buildJumpScript({ sessionId: "" })).toBe("");
  });

  test("attached：client 探测分支 + 单引号包裹的 tmux 目标 + 聚焦导航链", () => {
    const script = buildJumpScript(
      sess("ses_a", { tmuxPane: "%5", tmuxSession: "$29", pid: 123, status: "BUSY" }),
    );
    // $29 / %5 必须单引号包裹，防 sh 展开成位置参数
    expect(script).toContain(`__octl_sess='$29'`);
    expect(script).toContain(`__octl_pane='%5'`);
    // 有 client 分支：client tty 探测 → PPID 上溯 → focuswindow → 导航三连
    expect(script).toContain(`tmux list-clients -t "$__octl_sess" -F '#{client_tty}'`);
    expect(script).toContain(`hyprctl clients -j`);
    // focuswindow 双语法：旧语法（hyprlang 配置）失败时回退 Lua 表达式
    // （Hyprland 0.56 Lua 配置管理器下 dispatch 走 hl.dispatch Lua 桥）
    expect(script).toContain(`hyprctl dispatch focuswindow "pid:$__octl_win"`);
    expect(script).toContain(`hyprctl dispatch "hl.dsp.focus({window=\\"pid:$__octl_win\\"})"`);
    expect(script).toContain(`tmux switch-client -c "$__octl_ctty" -t "$__octl_sess"`);
    expect(script).toContain(`tmux select-window -t "$__octl_pane"`);
    expect(script).toContain(`tmux select-pane -t "$__octl_pane"`);
    // 无 client 分支：拉终端 attach
    expect(script).toContain(`omarchy launch terminal tmux attach -t "$__octl_sess"`);
    // 分支结构存在
    expect(script).toContain("if [ -n \"$__octl_ctty\" ]; then");
    expect(script).toContain("else");
  });

  test("detached：直接拉终端附着既有 tmux session", () => {
    const script = buildJumpScript(
      sess("ses_a", { tmuxPane: "%7", tmuxSession: "$31", pid: 0, status: "ERROR" }),
    );
    expect(script.trim()).toBe(`omarchy launch terminal tmux attach -t '$31' 2>/dev/null`);
    expect(script).not.toContain("switch-client");
    expect(script).not.toContain("hyprctl");
  });

  test("bare：pid PPID 上溯聚焦窗口，上溯失败回落 dead 行为", () => {
    const script = buildJumpScript(sess("ses_a", { pid: 4242, status: "RETRY" }));
    expect(script).toContain(`__octl_p="4242"`);
    expect(script).toContain(`hyprctl clients -j`);
    // focuswindow 双语法 fallback（同 attached）
    expect(script).toContain(`hyprctl dispatch focuswindow "pid:$__octl_p"`);
    expect(script).toContain(`hyprctl dispatch "hl.dsp.focus({window=\\"pid:$__octl_p\\"})"`);
    expect(script).toContain(`ps -o ppid= -p "$__octl_p"`);
    expect(script).toContain("exit 0");
    // 上溯穷尽（headless：opencode serve / 后台 run，祖先链无 Hyprland 窗口）
    // → 回落 dead 行为：重建/复用 tmux session 拉起查看实例
    expect(script).toContain(`__octl_name='tsesa'`);
    expect(script).toContain(`omarchy launch terminal tmux attach -t "$__octl_name"`);
  });

  test("dead：has-session/new-session/attach，--session 非 run，目录单引号", () => {
    const script = buildJumpScript(
      sess("ses_dead", { title: "Fix: daemon sync", pid: 0, status: "BUSY", directory: "/home/u/my repo" }),
    );
    expect(script).toContain(`__octl_name='Fixdaemonsync'`);
    expect(script).toContain(`if ! tmux has-session -t "$__octl_name" 2>/dev/null; then`);
    expect(script).toContain(`tmux new-session -d -s "$__octl_name" -c '/home/u/my repo'`);
    expect(script).toContain(`'opencode --session ses_dead'`);
    // 禁用 opencode run：非交互缺 message 即退，session 建立即死
    expect(script).not.toContain("opencode run");
    expect(script).toContain(`omarchy launch terminal tmux attach -t "$__octl_name"`);
  });

  test("dead：无 directory 时省略 -c", () => {
    const script = buildJumpScript(sess("ses_dead", { title: "T", pid: 0, directory: "" }));
    expect(script).toContain(`tmux new-session -d -s "$__octl_name" 'opencode --session ses_dead'`);
    expect(script).not.toContain(" -c ");
  });

  test("危险字符经 shQuote 转义，不逃出脚本", () => {
    const script = buildJumpScript(
      sess("ses_q", { title: "x", pid: 0, directory: "/tmp/a'b\nc" }),
    );
    // 单引号被转义、换行被替换，不会提前闭合脚本字符串
    expect(script).toContain(`-c '/tmp/a'\\''b c'`);
  });
});
