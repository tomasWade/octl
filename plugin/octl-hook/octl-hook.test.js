// @ts-check
import { describe, test, expect, mock, beforeEach, afterEach, spyOn } from "bun:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const SOURCE_PATH = resolve(__dirname, "../../internal/plugins/templates/octl-hook.js");
const sourceCode = readFileSync(SOURCE_PATH, "utf-8");

/** The 11 event types forwarded by the plugin. */
const TARGET_EVENTS = [
  "session.status",
  "session.idle",
  "session.created",
  "session.deleted",
  "session.error",
  "permission.asked",
  "permission.replied",
  "question.asked",
  "question.replied",
  "question.rejected",
  "session.compacted",
];

function isTargetEvent(type) {
  return TARGET_EVENTS.includes(type);
}

function createBuffer() {
  const CAPACITY = 500;
  const buf = [];
  return {
    push(evt) {
      if (buf.length >= CAPACITY) buf.shift();
      buf.push(evt);
    },
    items() {
      return [...buf];
    },
    drain() {
      const copy = [...buf];
      buf.length = 0;
      return copy;
    },
    size() {
      return buf.length;
    },
  };
}

describe("octl-hook event filtering", () => {
  test("all 11 target event types are defined", () => {
    expect(TARGET_EVENTS).toHaveLength(11);
  });

  test("question events are forwarded (opencode question tool)", () => {
    // 提问等待态只通过 question.* 事件表达（session.status 只有 idle/retry/busy），
    // 不转发则 daemon 无法感知提问等待。
    expect(isTargetEvent("question.asked")).toBe(true);
    expect(isTargetEvent("question.replied")).toBe(true);
    expect(isTargetEvent("question.rejected")).toBe(true);
  });

  test("every target event type is recognized", () => {
    for (const t of TARGET_EVENTS) {
      expect(isTargetEvent(t)).toBe(true);
    }
  });

  test("unknown event types are rejected", () => {
    expect(isTargetEvent("")).toBe(false);
    expect(isTargetEvent("foo.bar")).toBe(false);
    expect(isTargetEvent("session.nonexistent")).toBe(false);
    expect(isTargetEvent("session")).toBe(false);
    expect(isTargetEvent("permission.denied")).toBe(false);
    expect(isTargetEvent("message.created")).toBe(false);
    expect(isTargetEvent("step.start")).toBe(false);
    expect(isTargetEvent("tool.executed")).toBe(false);
  });

  test("partial prefix matches are rejected", () => {
    expect(isTargetEvent("session.xyz")).toBe(false);
    expect(isTargetEvent("session.status-extended")).toBe(false);
  });
});

describe("octl-hook buffer FIFO behavior", () => {
  test("buffer preserves insertion order (FIFO)", () => {
    const buf = createBuffer();
    buf.push({ type: "session.created", id: 1 });
    buf.push({ type: "session.status", id: 2 });
    buf.push({ type: "session.idle", id: 3 });

    const items = buf.drain();
    expect(items).toHaveLength(3);
    expect(items[0].id).toBe(1);
    expect(items[1].id).toBe(2);
    expect(items[2].id).toBe(3);
  });

  test("empty buffer drains to empty array", () => {
    const buf = createBuffer();
    expect(buf.drain()).toEqual([]);
    expect(buf.size()).toBe(0);
  });

  test("buffer holds up to 500 items without eviction", () => {
    const buf = createBuffer();
    for (let i = 0; i < 500; i++) {
      buf.push({ idx: i });
    }
    expect(buf.size()).toBe(500);

    const items = buf.drain();
    expect(items).toHaveLength(500);
    expect(items[0].idx).toBe(0);
    expect(items[499].idx).toBe(499);
  });

  test("buffer overflow evicts the single oldest item", () => {
    const buf = createBuffer();
    for (let i = 0; i < 501; i++) {
      buf.push({ idx: i });
    }
    expect(buf.size()).toBe(500);

    const items = buf.drain();
    expect(items).toHaveLength(500);
    expect(items[0].idx).toBe(1);
    expect(items[499].idx).toBe(500);
  });

  test("drain empties the buffer", () => {
    const buf = createBuffer();
    buf.push({ a: 1 });
    buf.push({ a: 2 });

    const first = buf.drain();
    expect(first).toHaveLength(2);
    expect(buf.size()).toBe(0);

    const second = buf.drain();
    expect(second).toEqual([]);
  });
});

describe("octl-hook source structure", () => {
  test("source imports { homedir } from 'node:os'", () => {
    expect(sourceCode).toMatch(/import\s*\{\s*homedir\s*\}\s*from\s*["']node:os["']/);
  });

  test("socketPath uses homedir()", () => {
    expect(sourceCode).toMatch(/homedir\(\s*\)/);
    expect(sourceCode).toMatch(/socketPath\s*=\s*homeDir\s*\+/);
  });

  test("default export has id 'octl-hook'", () => {
    expect(sourceCode).toMatch(/id:\s*["']octl-hook["']/);
  });

  test("server() returns event and dispose handlers", () => {
    expect(sourceCode).toMatch(/return\s*\{\s*event:/);
    expect(sourceCode).toMatch(/dispose:/);
  });

  test("data callback ignores daemon responses", () => {
    expect(sourceCode).toMatch(/data\s*\(\s*\)\s*\{/);
    expect(sourceCode).toMatch(/ignore daemon responses/);
  });

  test("source contains protocol version placeholder constant", () => {
    expect(sourceCode).toMatch(/const OCTL_PROTOCOL_VERSION = "{{OCTL_MD5}}"/);
  });
});

describe("octl-hook plugin lifecycle", () => {
  test("plugin can be instantiated and disposed", async () => {
    const mod = await import("../../internal/plugins/templates/octl-hook.js");
    const instance = await mod.default.server();
    expect(typeof instance.event).toBe("function");
    expect(typeof instance.dispose).toBe("function");
    await instance.dispose();
  });
});

describe("octl-hook process info payload", () => {
  const originalTMUXPane = process.env.TMUX_PANE;
  let connectSpy;
  let writeMock;

  beforeEach(() => {
    writeMock = mock(() => {});
    connectSpy = spyOn(Bun, "connect").mockImplementation(() =>
      Promise.resolve({
        write: writeMock,
        end: () => {},
        close: () => {},
      })
    );
  });

  afterEach(() => {
    connectSpy.mockRestore();
    process.env.TMUX_PANE = originalTMUXPane;
  });

  test("event payload contains pid field", async () => {
    delete process.env.TMUX_PANE;
    const mod = await import("../../internal/plugins/templates/octl-hook.js");
    const instance = await mod.default.server();

    await instance.event({
      event: { type: "session.status", properties: { sessionID: "abc" } },
    });

    expect(writeMock).toHaveBeenCalledTimes(1);
    const payload = JSON.parse(writeMock.mock.calls[0][0]);
    expect(payload.type).toBe("session.status");
    expect(payload.properties.sessionID).toBe("abc");
    expect(payload.properties.pid).toBe(process.pid);
    expect(payload.properties.tmuxPane).toBeNull();
    expect(payload.properties.tmuxSession).toBeNull();

    await instance.dispose();
  });

  test("payload contains tmuxPane when TMUX_PANE is set", async () => {
    process.env.TMUX_PANE = "%42";
    const mod = await import("../../internal/plugins/templates/octl-hook.js");
    const instance = await mod.default.server();

    await instance.event({
      event: { type: "session.status", properties: { sessionID: "abc" } },
    });

    expect(writeMock).toHaveBeenCalledTimes(1);
    const payload = JSON.parse(writeMock.mock.calls[0][0]);
    expect(payload.properties.tmuxPane).toBe("%42");
    expect(payload.properties).toHaveProperty("tmuxSession");

    await instance.dispose();
  });
});
