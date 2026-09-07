// OctlSidebar 离屏渲染测试：用 @opentui/solid 的 testRender 真实渲染组件，
// captureCharFrame 断言视觉输出——补上"纯函数全绿但界面不渲染"的验证盲区。
/** @jsxImportSource @opentui/solid */
import { describe, test, expect, afterAll } from "bun:test";
import { testRender } from "@opentui/solid";
import { createSignal } from "solid-js";
import { OctlSidebar, normalizeProject } from "../../internal/plugins/templates/octl-sidebar.tsx";
import type { SidebarProject } from "../../internal/plugins/templates/octl-sidebar.tsx";

// 与线上 sidebar 面板同量级的画布（约 33 列宽）。
const WIDTH = 40;
const HEIGHT = 30;

function mkProject(id: string, sessions: any[]): SidebarProject {
  return normalizeProject({
    projectId: id,
    worktree: `/home/u/${id}`,
    rowStatus: "IDLE",
    sessions,
  });
}

function mkRawSession(id: string, title: string, status: string, parentId = "") {
  return {
    sessionId: id,
    title,
    directory: "/tmp",
    timeUpdated: Date.now() - 5 * 1000,
    status,
    rowStatus: status,
    parentId,
    hasChildren: false,
    depth: parentId ? 1 : 0,
  };
}

const projects = [
  mkProject("alpha", [
    mkRawSession("ses_a1", "写周报", "BUSY"),
    mkRawSession("ses_a2", "旧任务", "IDLE"),
  ]),
  mkProject("beta", [
    mkRawSession("ses_b1", "等权限", "PERMISSION"),
    mkRawSession("ses_b2", "出错啦", "ERROR"),
  ]),
];

const noop = () => {};
const props = {
  connected: true,
  error: "",
  favorites: () => ({} as Record<string, boolean>),
  toggleFavorite: noop,
  onConfirmDelete: noop,
  onFocusSession: noop,
};

let setups: Awaited<ReturnType<typeof testRender>>[] = [];
afterAll(async () => {
  for (const s of setups) {
    try {
      s.renderer.stop();
    } catch {}
  }
});

async function renderSidebar() {
  const setup = await testRender(() => (
    <OctlSidebar projects={projects} {...props} />
  ), { width: WIDTH, height: HEIGHT });
  setups.push(setup);
  await setup.flush();
  return setup;
}

// 逐行去尾随空格，便于断言与阅读。
function frameLines(frame: string): string[] {
  return frame.split("\n").map((l) => l.replace(/\s+$/, ""));
}

describe("OctlSidebar 渲染（离屏真实渲染）", () => {
  // 冒烟：验证 bunfig.toml 的 solid-preload 生效（无 preload 时 bun 把 solid-js
  // 解析到 SSR 构建，信号更新不会重渲染，下面的转换测试会静默漏过）。
  test("solid 响应式构建生效（preload 冒烟）", async () => {
    const [on, setOn] = createSignal(false);
    const setup = await testRender(
      () => <text>{on() ? "STATE_ON" : "STATE_OFF"}</text>,
      { width: WIDTH, height: 5 },
    );
    setups.push(setup);
    await setup.renderOnce();
    const before = setup.captureCharFrame();
    setOn(true);
    await setup.renderOnce();
    expect(before).toContain("STATE_OFF");
    expect(setup.captureCharFrame()).toContain("STATE_ON");
  });

  test("tab 栏与状态过滤栏渲染在顶部", async () => {
    const setup = await renderSidebar();
    const frame = setup.captureCharFrame();
    const lines = frameLines(frame);
    // 前 6 行应包含：标题、tab 栏、过滤栏 chip（BSY/IDL/ERR/ASK 非零）
    const head = lines.slice(0, 8).join("\n");
    expect(head).toContain("Session Status");
    expect(head).toContain("全部");
    expect(head).toContain("收藏");
    expect(head).toContain("BSY 1");
    expect(head).toContain("IDL 1");
    expect(head).toContain("ERR 1");
    expect(head).toContain("ASK 1");
  });

  test("project 树渲染在过滤栏之下", async () => {
    const setup = await renderSidebar();
    const frame = frameLines(setup.captureCharFrame()).filter((l) => l.trim() !== "");
    const chipRow = frame.findIndex((l) => l.includes("BSY"));
    const treeRow = frame.findIndex((l) => l.includes("alpha"));
    expect(chipRow).toBeGreaterThanOrEqual(0);
    expect(treeRow).toBeGreaterThan(chipRow);
  });

  // 复现生产 bug：sidebar 挂载时 projects 为空（数据经 socket 异步到达）。
  // Solid 组件函数体只执行一次，若挂载期依据响应式数据 early-return null，
  // 数据到达后组件不会重跑 → 过滤栏永远不渲染。这是线上"看不见过滤按钮"
  // 的根因，静态数据的测试永远测不出来。
  test("空数据挂载后数据到达，过滤栏必须出现（mount-time null 陷阱）", async () => {
    const [proj, setProj] = createSignal<SidebarProject[]>([]);
    const setup = await testRender(
      () => (
        <OctlSidebar projects={proj()} connected={true} error="" favorites={() => ({})} toggleFavorite={noop} onConfirmDelete={noop} onFocusSession={noop} />
      ),
      { width: WIDTH, height: HEIGHT },
    );
    setups.push(setup);
    await setup.flush();
    // 挂载时无数据：过滤栏可以不显示
    expect(frameLines(setup.captureCharFrame()).some((l) => l.includes("BSY"))).toBe(false);
    // 模拟 daemon view 推送到达
    setProj(projects);
    await setup.flush();
    const lines = frameLines(setup.captureCharFrame());
    expect(lines.some((l) => l.includes("BSY 1"))).toBe(true);
    expect(lines.some((l) => l.includes("ASK 1"))).toBe(true);
  });
});
