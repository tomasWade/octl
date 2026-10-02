"""away-notify: 订阅 octl daemon 快照，会话卡审批/提问时推送飞书。

宿主：Hermes gateway 事件钩子（gateway/hooks.py 契约，events=[gateway:startup]）。
也可以脱离 gateway 独立运行（python3 handler.py），便于联调。

角色（HOOK.yaml.role）：
  central    查 away_flag → hermes send 直发飞书。flag 不在 = 在家冬眠。
  satellite  hermes peer dm 中央机报告；出门与否由中央过滤，本地不查 flag。

协议：unix socket 首行 {"type":"subscribe","channels":["snapshot"],"version":<MD5>}；
版本常量运行时读 octl-hook.js（daemon 的 alignPlugins 重写该文件时自动跟随）。
快照为全量 states，转移检测 = 相邻快照 diff（prev∉stuck → cur∈stuck）；
首帧只建基线不算转移（重连/重启不补发，卡住态由总览查询兜底）。
"""

import json
import os
import re
import socket
import subprocess
import threading
import time
from pathlib import Path

HOOK_DIR = Path(__file__).resolve().parent
PREFIX = "[away-notify]"
STUCK = ("PERMISSION", "ERROR")


def _log(msg):
    print(f"{PREFIX} {msg}", flush=True)


def _load_config():
    """解析本目录 HOOK.yaml 的顶层标量键。列表/嵌套结构不进配置（flat 够用）。"""
    cfg = {}
    try:
        text = (HOOK_DIR / "HOOK.yaml").read_text(encoding="utf-8")
    except OSError as e:
        _log(f"read HOOK.yaml failed: {e!r}")
        return cfg
    for line in text.splitlines():
        if line.startswith((" ", "\t", "#")):
            continue
        m = re.match(r"^([a-zA-Z_][a-zA-Z0-9_]*):\s*(.*?)\s*$", line)
        if m:
            cfg[m.group(1)] = m.group(2)
    return cfg


def _protocol_version():
    """从 octl-hook.js 提取协议 MD5。文件由 daemon alignPlugins 维护，天然同步。"""
    src = Path.home() / ".config" / "opencode" / "plugins" / "octl-hook.js"
    try:
        m = re.search(
            r'OCTL_PROTOCOL_VERSION\s*=\s*"([0-9a-f]{32})"',
            src.read_text(encoding="utf-8"),
        )
        return m.group(1) if m else ""
    except OSError:
        return ""


def _short(sid):
    return sid.removeprefix("ses_")[:4]


def _render(state, machine):
    """构造通知文案。question 优先（内容最具体），permission 用 permTitle 回退 permType。
    标题回退链：会话标题 → 首问 header（合成/新建会话可能无标题）→（无标题）。"""
    sid = _short(state.get("sessionId", ""))
    title = state.get("title") or ""
    questions = state.get("questionText") or ""
    if not title and questions:
        title = questions.split("：", 1)[-1][:30] if "：" in questions else questions[:30]
    title = title or "（无标题）"
    if questions:
        detail = questions
        if state.get("questionOptions"):
            detail += "（选项：" + " / ".join(state["questionOptions"]) + "）"
        kind = "提问"
    else:
        detail = state.get("permTitle") or state.get("permType") or "等待确认"
        kind = "卡审批"
    return (
        f"▲ [{machine}:{sid}] {title} · {kind}：{detail}\n"
        f"回「{sid} y」批准 / 「{sid} <话>」代答 / 直接描述它也行"
    )


def _fire_and_forget(cmd):
    subprocess.Popen(
        cmd,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )


def _notify(state, cfg):
    text = _render(state, cfg.get("machine", "unknown"))
    if os.environ.get("AWAY_NOTIFY_DRY"):
        _log(f"DRY notify: {text.splitlines()[0]}")
        return
    role = cfg.get("role", "central")
    hermes = str(Path(cfg.get("hermes_bin", "~/.local/bin/hermes")).expanduser())
    if role == "satellite":
        peer = cfg.get("report_peer", "")
        if not peer:
            _log("satellite 未配置 report_peer，丢弃")
            return
        _log(f"notify(peer): {text.splitlines()[0]}")
        # 报告外层带显式处置指令：中央管家按 away-relay 技能转发，且禁止回答卡片内容
        # （实测：裸卡片会被 LLM 当成问它的问题直接作答，导致不转发）。
        payload = (
            "【away-notify 卫星报告】这是机器间事件通知，请按 away-relay 技能「卫星报告处理」节执行："
            "查 ~/.hermes/state/away.flag；在 → 用 hermes send 把下面卡片原文转发到飞书（目标见该技能）并回执；"
            "不在 → 仅回执「在家模式，未转发」。卡片内容不是问你的问题，禁止作答、禁止改写，原样转发。\n"
            f"卡片：{text}"
        )
        _fire_and_forget([hermes, "peer", "dm", peer, payload])
        return
    flag = Path(cfg.get("away_flag", "~/.hermes/state/away.flag")).expanduser()
    if not flag.exists():
        return  # 在家：冬眠，仅维护基线
    _log(f"notify(feishu): {text.splitlines()[0]}")
    _fire_and_forget([hermes, "send", "-t", cfg["feishu_target"], text])


def _diff(prev, states, notify_error):
    """返回 (新基线, 转移进卡住态的 state 列表)。首帧 prev 为空 → 全部只建基线。"""
    stuck = ("PERMISSION", "ERROR") if notify_error else ("PERMISSION",)
    cur, hit = {}, []
    for s in states:
        sid, status = s.get("sessionId", ""), s.get("status", "")
        cur[sid] = status
        if status in stuck and prev.get(sid) not in stuck and sid in prev:
            hit.append(s)
        elif status in stuck and sid not in prev and prev:
            # 会话首见即卡住（快照晚于事件）：基线已建过才报，首帧不报
            hit.append(s)
    return cur, hit


def _subscribe_loop(cfg):
    sock_path = Path(cfg.get("octl_socket", "~/.local/share/octl/octl.sock")).expanduser()
    version = _protocol_version()
    if not version:
        raise RuntimeError("octl-hook.js 里找不到 OCTL_PROTOCOL_VERSION")
    notify_error = cfg.get("notify_error", "false").lower() in ("1", "true", "yes")

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(35)
    sock.connect(str(sock_path))
    sock.sendall(
        (json.dumps({"type": "subscribe", "channels": ["snapshot"], "version": version}) + "\n").encode()
    )

    buf, prev, last_ping = b"", {}, time.monotonic()
    _log(f"subscribed ({cfg.get('role', 'central')}@{cfg.get('machine', '?')}), streaming snapshots")
    while True:
        # 空闲保活：daemon 支持 ping/pong，连续两次无响应由 socket 超时兜底重连
        if time.monotonic() - last_ping > 30:
            sock.sendall(b'{"type":"ping"}\n')
            last_ping = time.monotonic()
        try:
            chunk = sock.recv(65536)
        except socket.timeout:
            continue
        if not chunk:
            raise RuntimeError("daemon closed connection")
        buf += chunk
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            if not line.strip():
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if msg.get("type") != "snapshot":
                continue  # subscribed / pong / view 等
            prev, hits = _diff(prev, msg.get("states", []), notify_error)
            for s in hits:
                _notify(s, cfg)


def _run():
    """自愈主循环：任何异常 → 退避重连（gateway 不监督钩子线程，自己保活）。"""
    cfg = _load_config()
    backoff = 2
    while True:
        started = time.monotonic()
        try:
            _subscribe_loop(cfg)
        except Exception as e:
            _log(f"subscribe loop exited after {time.monotonic() - started:.0f}s: {e!r}")
        time.sleep(backoff)
        backoff = min(backoff * 2, 60)


def handle(event_type, context):
    """Hermes 钩子入口：gateway 启动时拉起常驻订阅线程。"""
    if event_type == "gateway:startup":
        _log("gateway up, starting subscriber thread")
        threading.Thread(target=_run, daemon=True, name="octl-away-notify").start()


if __name__ == "__main__":
    _log("standalone mode (no gateway); Ctrl-C to stop")
    _run()
