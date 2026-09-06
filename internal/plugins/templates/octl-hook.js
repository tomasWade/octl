const OCTL_PROTOCOL_VERSION = "{{OCTL_MD5}}";

import { homedir } from "node:os";
import { execSync } from "node:child_process";

export default {
  id: "octl-hook",
  server: async () => {
    const homeDir = homedir();
    const socketPath = homeDir + "/.local/share/opencode/octl.sock";

    const targetEvents = [
      "session.status", "session.idle", "session.created",
      "session.deleted", "session.error",
      "permission.asked", "permission.replied",
      "question.asked", "question.replied", "question.rejected",
      "session.compacted",
    ];

    let socket = null;
    let buffer = [];
    let retryTimer = null;
    let disposed = false;

    // 读取当前 tmux session id（如 "$0"）。未附着 tmux（无 TMUX_PANE）或调用
    // 失败时返回 null，daemon 按未附着处理。
    function getTMUXSession() {
      if (!process.env.TMUX_PANE) return null;
      try {
        const out = execSync('tmux display-message -p "#{session_id}"', {
          encoding: "utf-8",
          timeout: 500,
        });
        return out.trim() || null;
      } catch {
        return null;
      }
    }

    function scheduleRetry() {
      if (disposed || retryTimer) return;
      retryTimer = setTimeout(() => { retryTimer = null; tryConnect(); }, 2000);
    }

    function onSocketClose() {
      socket = null;
      if (!disposed) scheduleRetry();
    }

    async function tryConnect() {
      if (disposed) return;
      try {
        socket = await Bun.connect({
          unix: socketPath,
          socket: {
            data() { /* ignore daemon responses */ },
            close: onSocketClose,
            error: onSocketClose,
          },
        });
      } catch (e) {
        scheduleRetry();
        return;
      }
      while (buffer.length > 0) {
        const evt = buffer[0];
        try { socket.write(JSON.stringify(evt) + "\n"); buffer.shift(); }
        catch { onSocketClose(); return; }
      }
    }

    function closeConnection() {
      disposed = true;
      if (retryTimer) { clearTimeout(retryTimer); retryTimer = null; }
      if (socket) { try { socket.end(); } catch {} try { socket.close(); } catch {} socket = null; }
    }

    function bufferEvent(evt) {
      if (buffer.length >= 500) buffer.shift();
      buffer.push(evt);
    }

    tryConnect();

    return {
      event: async ({ event }) => {
        if (!targetEvents.includes(event.type)) return;

        // 附带进程/tmux 上下文：daemon 以 pid 维护 session→tmux pane 映射，
        // 连接断开时按 pid 清理。未附着 tmux 时 tmuxPane/tmuxSession 为 null。
        const enriched = {
          ...event,
          properties: {
            ...(event.properties || {}),
            pid: process.pid,
            tmuxPane: process.env.TMUX_PANE || null,
            tmuxSession: getTMUXSession(),
          },
        };

        if (socket) {
          try { socket.write(JSON.stringify(enriched) + "\n"); return; }
          catch { bufferEvent(enriched); onSocketClose(); return; }
        }
        bufferEvent(enriched);
        if (!retryTimer && !disposed) scheduleRetry();
      },
      dispose: async () => { closeConnection(); },
    };
  },
};
