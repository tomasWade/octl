# octl — opencode Session Manager

English | [中文](README.md)

[![CI](https://github.com/tomasWade/octl/actions/workflows/ci.yml/badge.svg)](https://github.com/tomasWade/octl/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A terminal TUI + daemon for browsing and managing your local [opencode](https://opencode.ai) sessions — real-time status, favorites, stats and daily digests. Fully offline; SQLite is only ever opened read-only.

octl gives you a single terminal view of every opencode session on your machine: what's running, what's waiting for a permission reply, what errored out, and what you actually got done today.

## ✨ Features

- **🧩 opencode sidebar integration (flagship)** — a live panel docked on the right side of the opencode TUI, so you never leave the conversation you're writing: a **session tree across all projects**, aggregated status dots (a parent turns 🟡 the moment any child needs attention), status-chip filtering, two-level folding, and full mouse control — click a title to favorite (★), click ↗ to jump straight to that session's tmux pane, click ✕ to delete. One glance tells you which agent is waiting on you
- **Terminal TUI console** — a project/session tree with multi-select batch delete / export, favorites (★ persisted), create / fork / send message — all routed through the daemon via the opencode CLI
- **Real-time status overview** — BUSY / RETRY / IDLE / PERMISSION / ERROR / ARCHIVED pushed live; permission waits (🟡) are visible at a glance, no more forgotten agents
- **📊 Usage stats** — total sessions / active sessions / cost / tokens
- **📅 Daily raw digest** — `octl report` writes a mechanical fact layer (active lines + user-message skeletons + deletion obituaries) for skills / agents to narrate (see a full consumer example in [examples/skills](examples/skills/六耳/SKILL.md))
- **🔒 Safe** — fully offline; SQLite opened read-only via `?mode=ro`; deletions go through `opencode session delete`, never direct DB writes

The sidebar panel embedded in opencode (real recording — "Favorites / All" tab switching, the favorites list, and a ↗ click jumping straight to the tmux window where "run e2e tests" lives):

![octl sidebar demo](docs/img/demo-sidebar.gif)

The standalone terminal console (`octl`, animated):

![octl TUI demo](docs/img/demo.gif)

## 🚀 Quick Start

```bash
go install github.com/tomasWade/octl@latest
octl plugins --output=~/.config/opencode/plugins/   # generate opencode plugins (recommended)
octl --daemon &                                     # start the daemon
octl                                                # open the TUI
```

Requirements: Go 1.25+, and existing opencode data on the machine (`~/.local/share/opencode/opencode.db`). The sidebar panel additionally needs the plugin registered in `~/.config/opencode/tui.json` (see "Installing the opencode plugins" below).

No Go toolchain? Grab a prebuilt binary from [Releases](https://github.com/tomasWade/octl/releases) (linux/darwin × amd64/arm64), put it on your `PATH`; see "Keeping the daemon running" below for making it persistent.

## 💬 Example Output

<details>
<summary><code>octl query snaps</code> — live status of every session</summary>

```text
🔵 BUSY  refactor auth module    myproj    13s  abc123def456...
🟢 IDLE  fix login timeout       global    14s  f9964bbacffe...
🟢 IDLE  weekly report           global    2m   f8c16b5d2ffe...
🟡 ASK   db migration script     myproj    5m   f8bc4b454ffe...
⚪ IDLE  Greeting message        global    1h   f8bc40890ffe...
```

</details>

<details>
<summary><code>octl query daily --json</code> — time-window activity aggregation (for scripts / skills)</summary>

```json
{
  "daily": {
    "projects": [
      {
        "name": "myproj",
        "newSessions": [{ "title": "add e2e tests", "timeCreated": 1757088000000 }],
        "activeSessions": [
          {
            "title": "refactor auth module",
            "msgCount": 42,
            "firstUserExcerpt": "Split the auth middleware into its own module, check dependencies first…",
            "lastAssistantExcerpt": "Split complete, all tests pass…"
          }
        ],
        "archivedSessions": [],
        "sessionCostSum": 1.24
      }
    ],
    "zombies": [],
    "stuckStates": [{ "sessionId": "ses_f8bc4b454", "status": "PERMISSION" }]
  }
}
```

</details>

<details>
<summary><code>octl report</code> — <code>daily/2026-09-05.raw.md</code> raw digest</summary>

```markdown
<!-- stats: {"from":1757068800000,"to":1757155200000,"projects":1,"active":6,"messages":87} -->
# 2026-09-05

## myproj

### refactor auth module (ses_abc123def)
- messages: 42 | window: 09:12–18:40 | cost: $1.24
- first: Split the auth middleware into its own module, check dependencies first…
- last: Split complete, all tests pass…

#### user message skeleton
- 09:12 Split the auth middleware into its own module, check dependencies first
- 10:40 middleware split done, add e2e tests
- 16:05 CI failed on one case, take a look
```

</details>

## ⚙️ How It Works

octl consists of two processes, a **daemon** and a **TUI**: all data reading, status derivation and management actions live in the daemon (the single source of truth); the TUI and the sidebar plugin only render, receiving the full view over a Unix socket.

```
opencode instances ──hook plugin forwards 11 event types──┐
                                                          ▼
SQLite (read-only) ──▶ octl daemon ──ViewMsg──▶ octl TUI / sidebar plugin
```

## Starting octl

1. **Start the daemon first** (runs in the foreground, friendly to supervisor/systemd):

   ```bash
   ./octl --daemon
   ```

2. **Then start the TUI** (the default behavior):

   ```bash
   ./octl
   # or explicitly
   ./octl --tui
   ```

On startup the TUI connects to the daemon's Unix socket. If the daemon isn't running, the TUI won't exit — it shows `🔴 OFFLINE` in the top-right corner and retries every 5 seconds; once the daemon is back it shows `🟢 ONLINE` and resumes.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--daemon` | `false` | Run the daemon service in the foreground |
| `--tui` | `false` | Run the TUI explicitly (default) |
| `--version` | `false` | Print version + protocol MD5 and exit |
| `--socket` | `~/.local/share/octl/octl.sock` | Unix socket path |
| `--refresh-time` | `5` | Deprecated; refresh is daemon-push driven, the TUI no longer polls |

Subcommands: `plugins` (generate opencode plugins), `query` (one-shot queries), and four action subcommands `delete` / `create` / `fork` / `send` (see the next two sections).

## One-shot Queries (`octl query`)

Query the daemon once and print the result — ideal for scripts or a quick status check from the terminal. Requires the daemon to be running (stdout carries only the result; human-readable messages go to stderr; exit codes `0` ok / `1` runtime error / `2` usage error).

```bash
octl query snaps                          # live status of all sessions (supervisorctl-style table)
octl query sessions                       # project/session tree (with aggregated root status)
octl query messages <sessionId>           # conversation content; sessionId supports fuzzy matching
octl query daily                          # activity aggregation for a time window (defaults to today)
```

The default output is a human-readable table (colored status dot + status word, title, project, relative age, lowercased session ID without prefix). `--json` emits the full JSON for `jq` and friends:

```bash
octl query snaps --json | jq '.states[] | select(.status=="BUSY")'
```

**Fuzzy matching**: the `messages` sessionId argument is matched case-insensitively as a substring (with or without the `ses_` prefix); it runs only on a unique hit — multiple hits list the candidates, zero hits report not-found.

**`--nums` message selector** (messages only, default `0`):

| Syntax | Meaning |
|--------|---------|
| `1` / `2` / … | The 1st, 2nd… message (from the head) |
| `0` | The last message (default) |
| `-1` / `-2` | One / two before the last (from the tail) |
| `3-5`, `4--2`, `-2-6` | Ranges; descending forms are swapped to ascending |
| `1,3,-1` | Comma list, deduplicated, output in time order |
| `all` | The full conversation |

Out-of-range values are clamped: positive overflow selects the last message, negative overflow the first.

```bash
octl query messages 5v1n --nums all       # full conversation
octl query messages 5v1n --nums 1,3-5,-1  # 1st, 3rd–5th, plus the second-to-last
```

**`daily` time-window aggregation** returns the facts inside a window — new / active / archived sessions grouped by project (active sessions carry message counts, first/last activity times and excerpts), zombie sessions (idle > 48h with activity in the last 30 days), and sessions currently stuck in PERMISSION/ERROR in daemon memory. Excerpts are quota-truncated (top 100 active sessions globally; first user message 120 chars / last assistant message 500 chars); `totalActive`/`excerpted` in the JSON mark truncation — pull the full text for an individual session with `query messages` as needed. Window semantics: defaults to today (local calendar day); `--date 2026-09-04` selects a single day (mutually exclusive with `--from/--to`); `--from` may be used alone (`--to` defaults to now); time formats `2026-09-04` / `2026-09-04T14:00` / `2026-09-04 14:00`.

```bash
octl query daily                          # what happened today
octl query daily --date 2026-09-04        # a specific day
octl query daily --from 2026-09-01        # 09-01 → now (a weekly-report window)
octl query daily --from 2026-09-01 --to 2026-09-08 --json | jq '.daily.projects[] | {name, active: (.activeSessions | length)}'
```

Other flags: `--socket <path>` overrides the daemon socket path, `--timeout <sec>` sets the response timeout (default 5s). Run `octl query` with no arguments for the full help.

## One-shot Actions (`octl delete / create / fork / send`)

Send a management action to the daemon and synchronously wait for the result — the same action protocol the TUI uses. Requires the daemon to be running (stdout carries only the result; exit codes `0` ok incl. explicit cancel / `1` runtime error / `2` usage error).

```bash
octl delete <sessionId>...        # delete sessions (multiple allowed, fuzzy match)
octl create <message>             # create a session with an initial message
octl fork <sessionId> <message>   # fork a session with a new message
octl send <sessionId> <message>   # send a message to an existing session
```

```bash
octl delete 5v1n                  # delete (on a tty: lists targets and asks for confirmation)
octl delete 5v1n --yes            # skip confirmation (script-friendly)
octl delete abc123 def456 --json  # batch delete, JSON output
octl create "fix the login bug"   # create a session in the current directory
octl create "run tests" --dir ~/code/myproj
octl fork 5v1n "try a different approach"
octl send 5v1n "continue the task"
```

Behavior notes:

- **Fuzzy matching**: the same case-insensitive substring match as `query messages` (with or without `ses_` prefix), unique hits only; for batch deletes every input must resolve or nothing executes (any ambiguity fails the whole batch — no partial deletes).
- **Delete confirmation**: on a tty, targets are listed with a `[y/N]` prompt by default; `--yes` skips it; non-tty (scripts/pipes) executes directly. Deletion is synchronous — batch duration scales with count (`--timeout` default 60s).
- **Cascade delete**: CLI delete removes the target's entire subtree of child sessions (the action carries `cascade=true`; the daemon expands the tree by parent relations), leaving no orphaned data. The flag defaults to false; TUI/sidebar deletion behavior is unaffected (dashboard/sidebar already collect full descendants client-side; Favorites deletes only the selected entries).
- **Directories**: `create`'s `--dir` defaults to the current directory; when `--dir` is omitted for `fork` / `send`, the daemon automatically uses the session's recorded working directory — no need to specify.
- **Background execution**: `create` / `fork` / `send` spawn `opencode run` in the background on the daemon side and return immediately (the result means "started", not "finished"); track progress with `octl query snaps`.
- Common flags: `--json`, `--socket <path>`, `--timeout <sec>`; run any subcommand without arguments for its full help.

## Daily Raw Digest (`octl report`)

`octl report` asks the daemon to render the "raw digest" (mechanical fact layer) to `~/.local/share/octl/daily/`, for daily-report skills / agents to narrate further, or for plain human archaeology (a complete consumer example: [examples/skills](examples/skills/六耳/SKILL.md)):

```bash
octl report                          # today (overwrite)
octl report --date 2026-09-04        # backfill a day's final version
octl report --from 2026-09-01        # a range window (file name = start date)
```

- **`<date>.raw.md`**: metadata of the day's active sessions + verbatim user-message skeletons + a machine-readable stats line; fully overwritten each time (latest is truth).
- **`deleted/<date>.md`**: a full-history "obituary" written automatically before a session is deleted (append-only, never overwritten) — a deleted session's history survives the DB.
- **Automatic writes**: the daemon writes on startup and checks periodically (today's raw digest is refreshed if older than 4 hours; yesterday's final version is backfilled), fitting non-server schedules (write as soon as the machine boots).
- Delete sessions any time: the obituary mechanism guarantees zero history loss.

## The Real-time Daemon

octl ships a standalone daemon that receives live status events from opencode sessions over a Unix socket and acts as the single data center pushing the full view to TUI/sidebar.

**The daemon must be started separately**: `octl --daemon`. It listens on:

```
~/.local/share/octl/octl.sock
```

The daemon:
1. Fully syncs the SQLite database at startup, deriving an initial status for every session
2. Re-syncs the database every 30 seconds, correcting stale or lost event states
3. Receives 11 event types over the Unix socket (session.status, session.idle, session.created, session.deleted, session.error, permission.asked, permission.replied, question.asked, question.replied, question.rejected, session.compacted)
4. Pushes the full `ViewMsg` (projects / sessions / stats / favorites) to subscribed TUI and sidebar clients
5. Receives and executes management actions from the TUI (delete / export / create / fork / send / favorite / unfavorite); favorites are maintained by the daemon and persisted to `~/.local/share/octl/state.json` (restored on restart)

### Keeping the daemon running (systemd user service)

The repo ships a systemd user service example (no root needed):

```bash
mkdir -p ~/.config/systemd/user
cp scripts/octl.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now octl
```

Logs: `journalctl --user -u octl -f`. If the binary lives somewhere other than `~/go/bin`, adjust `ExecStart` in the unit.

### Installing the opencode plugins

octl relies on two opencode plugins working together:

#### 1. Server plugin (event forwarding)

Generate it into opencode's plugins directory:

```bash
octl plugins --output=~/.config/opencode/plugins/
```

Every opencode instance auto-loads it on startup. It will:
- Connect to the octl daemon over a Unix socket
- Filter and forward 11 target event types in real time
- Maintain a 500-entry FIFO buffer (buffers while the daemon is down, replays on connect)
- Reconnect every 2 seconds after a disconnect

> The plugin runs on the Bun runtime (built into opencode) with no external npm dependencies.

> Generated files carry a protocol version constant `OCTL_PROTOCOL_VERSION` matching the `octl` binary. If the plugin was generated by an older `octl`, the daemon rejects the subscription with `octl version mismatch — run "octl plugins" to regenerate` (the TUI shows the same hint top-right; the sidebar shows a version-mismatch error and stops retrying). Re-run `octl plugins --output=...` and **restart opencode** to align (a running opencode does not hot-reload plugin files; stale plugins keep being rejected).

#### 2. TUI plugin (the sidebar panel)

Register it via `~/.config/opencode/tui.json` — add it to the `plugin` array:

```json
{
  "$schema": "https://opencode.ai/tui.json",
  "plugin": [
    "file:///home/<user>/.config/opencode/plugins/octl-sidebar.tsx"
  ]
}
```

- Must be an absolute `file://` path.
- Restart the opencode TUI after changing the plugin file or updating.

> The current sidebar subscribes directly to the daemon-pushed `ViewMsg` and no longer sends `request/listSessions`.

### opencode Sidebar Integration

With both plugins installed and the daemon running, open any **session chat view** in opencode — the right sidebar shows the `Session Status` panel.

What you get:
- A top tab bar switching between "All" and "Favorites": the project/session tree in "All", the favorites list in "Favorites"; click to switch, the active tab is highlighted (`#c0caf5`), the inactive dimmed (`#565f89`)
- Each project renders as a group header: `▶/▼ 📁<worktree path> (N)` where N is the project's total session count
- Click a project header (left mouse) to fold/unfold its session list; the icon toggles between ▶ (folded) and ▼ (unfolded); fold state survives daemon refreshes
- Each root session shows a status icon, title and last-update time (e.g. `🔵 my-session · 2m`); sessions with subsessions carry a ▶/▼ icon at the line start — click **that icon** to fold/unfold children (clicking the title no longer toggles)
- Sessions with children default to folded, showing just the root line with a direct-child count (e.g. `▶ my-session · 2m (3)`); unfolded subsessions render indented with their own status icons
- **Favorites**: left-click a session title to favorite/unfavorite (favorites are owned by the daemon and persisted; TUI and sidebar stay consistent through it and survive restarts); favorited titles get a `★` prefix in yellow highlight. The favorites list lives in the "Favorites" tab (`★ Favorites`; entries resolve titles/status from all projects' sessions — parents render the aggregated `rowStatus` dot, leaves their own `status`, same as the tree — deleted sessions are skipped, clicking an entry unfavorites it); empty shows `(no favorites)`
- **Delete (X button)**: after the ★ at the line end comes the ↗ jump button, then the red X delete button; left-click opens a confirmation overlay (`[Confirm]` / `[Cancel]`); confirming executes via the daemon — deleting a session removes its entire subtree, deleting a project removes all its sessions and the project record; the global project shows no X and cannot be deleted
- **tmux jump (↗ button)**: the blue ↗ sits between ★ and X; favorites entries have one too. Click it: if the session is attached to tmux, the client switches straight to its pane; otherwise a new tmux session is created under the session's working directory (title cleaned, truncated to 15 chars) and opens `opencode --session <id>` in TUI mode. Requires opencode to be started inside tmux (the hook plugin reports pid/tmux coordinates; the daemon maintains the mapping and invalidates it when opencode exits)
- **Hover highlight**: hovering a session title tints it (light blue), reverting on leave
- The status icon reflects the highest-priority live status anywhere in that session's tree (aggregated whether folded or not):

  | Icon | Status | Meaning |
  |------|--------|---------|
  | 🔴 | ERROR | a session entered an error state |
  | 🟡 | ASK | a session is waiting on you (tool permission or agent question) |
  | 🟠 | RETRY | a session is retrying |
  | 🔵 | BUSY | the AI is responding / thinking |
  | ⚪ | IDLE | waiting for user input |
  | ◯ | UNKNOWN | status uncertain (derived from the database) |

- Shows `offline` when the daemon is down; `daemon not running` / `daemon not responding` on connection failures

> Note: the sidebar only renders once you enter a session view — it does not appear on the opencode home screen.

## TUI Views

| Key | View | Purpose |
|-----|------|---------|
| `1` | 📋 **Manage** | Project + session tree — sessions grouped by project, with management actions |
| `2` | ⭐ **Favorites** | Favorites list — favorited sessions (daemon-owned) |
| `3` | 📊 **Stats** | Usage stats — global totals (sessions / active / cost / tokens) |
| `Tab` | — | Next view |
| `Shift+Tab` | — | Previous view |

### Global keys

| Key | Action |
|-----|--------|
| `Ctrl+x` / `Ctrl+C` | Quit |
| `1`–`3` | Jump to a view |

---

## View Details

### Manage (key 1)

A tree of projects and sessions, grouped by project.

#### Tree layout

```
📁 global (12)         ← project node (with session count)
  ses_xxx...  my-session  architect  $0.05  5m ago  ⚪ IDLE     ← waiting for input
    ses_yyy...  subagent   reviewer   $0.01  5m ago  🔵 BUSY    ← AI working
📁 dbc11090 (0)        ← git-repo project (0 sessions)
```

The **Status** column shows the daemon-pushed live status (requires the octl-hook.js plugin in your opencode instances). Status meanings:

| Icon | Status | Meaning |
|------|--------|---------|
| 🔴 | ERROR | session in error state |
| 🟡 | ASK | session waiting: tool permission (bash / file write…) or agent question |
| 🟠 | RETRY | retrying after error |
| 🔵 | BUSY | AI responding / thinking |
| ⚪ | IDLE | waiting for user input |
| ◯ | ??? | status uncertain (derived from database) |
| &nbsp;&nbsp;— | ARCHIVED | archived |

When the daemon is down or the plugin is missing, the TUI shows `🔴 OFFLINE` top-right; the status column stops refreshing but keeps the last known states.

#### Key bindings

| Key | Action | Valid on |
|-----|--------|----------|
| `j` / `↓` | Move cursor down | all |
| `k` / `↑` | Move cursor up | all |
| `l` / `→` / `Enter` | Expand node | all |
| `h` / `←` | Collapse / go to parent | all |
| `Space` | Multi-select: toggle a session; on a project, select/deselect all descendant sessions | all |
| `d` | Delete: with a selection, batch-delete all selected sessions; otherwise delete the current subtree (project = batch delete its sessions; global deletes sessions only) | all |
| `e` | Export JSON to `/tmp/octl-exports` | all (project = batch export) |
| `m` | View conversation history (h/l to page, j/k to scroll, scrollbar shows position) | session nodes only |
| `n` | New session (on project) / fork (on session, prompts for first message) | project/session |
| `s` | Send a message to an existing session | session nodes only |
| `f` | Toggle favorite: all selected when multi-selecting, otherwise the cursor session (daemon-owned) | session nodes only |
| `r` | Reminder that refresh is daemon-driven (kept for compatibility) | all |

- Selected lines are highlighted in purple with a `✓` prefix; multi-selection survives refreshes.
- When the tree exceeds the viewport a vertical scrollbar appears (`█` = window, `░` = track), auto-scrolling to follow the cursor.
- The conversation viewer has a bottom scrollbar showing the current position percentage.

### Favorites (key 2)

Lists favorited sessions in `ViewMsg` order. Supports cursor navigation, multi-select, unfavorite and delete.

| Key | Action |
|-----|--------|
| `j` / `k` / `↓` / `↑` | Move cursor |
| `Space` | Multi-select: toggle the current session |
| `f` | Unfavorite: all selected when multi-selecting, otherwise the cursor session |
| `d` | Delete: all selected when multi-selecting, otherwise the cursor session (also removed from favorites) |

- Empty favorites show `No favorites yet — press f in Manage to favorite a session`.
- Line format: `status icon + title`; the cursor line gets a `▶` prefix; multi-selected lines get `✓` and purple highlight.

> Favorites are **daemon-owned**: the authoritative source is the daemon-pushed `ViewMsg.Favorites` plus each session's `isFavorite` field. Pressing `f` in the TUI or clicking a title in the sidebar does an optimistic local update, sends a `favorite` / `unfavorite` action, and the daemon reconciles by pushing a fresh `ViewMsg`; deleting a session prunes favorites automatically. TUI and sidebar stay consistent through the daemon. Favorites never touch opencode's database — persistence goes to the daemon's own `~/.local/share/octl/state.json` (write-on-change + periodic snapshots + flush on exit, restored on restart), with orphaned favorites cleaned up by existing pruning.

### Stats (key 3)

Global statistics: total sessions, active sessions, total cost, total tokens.

---

## Data Source

The daemon reads opencode's SQLite database at:

```
~/.local/share/opencode/opencode.db
```

octl touches four tables — `project`, `session`, `message`, `part` — with `SELECT`-only, read-only queries. The TUI never connects to the database directly; all displayed data comes from the daemon-pushed `ViewMsg`.

## Security

| Property | Details |
|----------|---------|
| **Read-only DB** | Opened with `?mode=ro`; any SQL write is rejected at the driver level |
| **CLI-mediated deletes** | No direct DB writes; session deletion goes through `opencode session delete`, orchestrated by the daemon |
| **Confirmation dialogs** | Delete/export actions require confirmation |
| **No network access** | Fully offline; nothing is sent anywhere |
| **Never touches your files** | Project deletion only cleans DB records; git repos and project files are untouched |
| **session_diff cleanup** | After a successful `DeleteSession`, octl best-effort cleans `~/.local/share/opencode/storage/session_diff/<sessionID>` |

## Building & Plugin Generation

```bash
# Build the binary
go build -o octl .

# Generate opencode plugins (octl-hook.js + octl-sidebar.tsx)
octl plugins --output=~/.config/opencode/plugins/
```

Generated plugins embed a protocol version constant matching the binary; regenerate after upgrading octl.

## Testing

```bash
# All Go unit tests (temp SQLite DBs, never touches real data)
go test ./...

# Plugin tests (requires Bun)
cd plugin && bun install && bun test
```

> Plugin tests import the TSX templates from `internal/plugins/templates/`, which resolves `@opentui/solid` via a repo-root `node_modules` symlink (pointing at `plugin/node_modules`). On a fresh clone run `ln -sfn plugin/node_modules node_modules` first (CI does this built-in).

## FAQ

**Why not opencode's built-in session list, or just more terminals?** octl aggregates live status across *all* your projects — which agents are stuck on a permission prompt, which errored, which have been idle how long — plus batch management, usage stats and daily digests. Multiple terminals see no global picture and keep no history. octl doesn't intrude on opencode itself (read-only DB + CLI deletes); use both freely.

## Known Limitations

- The TUI needs the daemon to browse data; when the daemon is offline only the offline hint is shown (no static offline browsing).
- `--refresh-time` is accepted but no longer effective — refresh is entirely daemon-push driven.
