# lazyrex

A terminal UI for the Rex multiplexer, in the style of lazygit and lazydocker: sessions, windows, blocks and clients in side panels, the selected item's capture or inspect output in the main view, and every `rex` command it runs in a command log.

It manages the **local** Rex server, **native hosts** reached through `rex -S SERVER` (config targets, `--server`, and the hosts `rex hosts list` discovers) and **SSH targets**, Rex servers on other machines reached through `ssh`, with server and client actions, the keymap, a command palette and `x` action menus.

## Install

Requires Go 1.26 and `rex` (API revision 9) in `PATH`. SSH targets also need OpenSSH's `ssh` in `PATH` and `$XDG_RUNTIME_DIR` set to a private directory (a clean absolute path, yours, mode 0700, no symlinks, short enough for socket paths); without it, or on Windows, SSH targets are listed but say why they are not available.

```sh
go install github.com/rbobillot/lazyrex/cmd/lazyrex@latest
# or, from a checkout:
go build -o /tmp/lazyrex ./cmd/lazyrex
```

## Usage

```sh
lazyrex [--server|-S TARGET | --ssh HOST] [--config PATH] [--version]
```

| Flag | Meaning |
|---|---|
| `--server TARGET`, `-S TARGET` | start on TARGET: a config target name, `local`, or anything else as an ad-hoc native target reached through `rex -S TARGET` (a `rex hosts` label, `host:port` or URL) |
| `--ssh HOST` | start on the SSH target reaching HOST, a `Host` of `~/.ssh/config` or `user@host`: the first config SSH target whose `ssh_host` is exactly HOST, else a new ad-hoc SSH target named HOST, in forward mode (refused if another target already has that name). It cannot be combined with `--server`. |
| `--config PATH` | config file; default `$XDG_CONFIG_HOME/lazyrex/config.yml`, else `~/.config/lazyrex/config.yml`. A missing default file means defaults; a missing `--config` file is an error. |
| `--version` | print the version and exit |

A bad config, a key bound twice in one panel, or a server or SSH destination lazyrex refuses exits with status 2 and says why. See [`config.example.yml`](config.example.yml) for every key.

## What it shows

- **[1] Targets**: the targets (see [Targets](#targets)), `*` on the active one, with its transport, status (`online`, `stopped`, `unreachable`, `unreachable (ssh)` when ssh itself failed, or `api mismatch` when the server's `api_revision` is not 9) and session count. A stopped local server is normal (it exits when its last session ends) and is never reported as an error. Main tabs: **Status** (`whoami` and `server status`, discovered host details and `rex hosts check` results) and **Keymap & Actions** (`rex keymap --json` and `rex actions --json`, each action tagged `[allowed]`, `[confirm]` or `[denied]` by the safety layer; read only for the active target).
- **[2] Sessions**: label, window count, block count. Main tab: **Inspect** (a summary, then the raw `session inspect` JSON).
- **[3] Windows**: windows of the selected session, or of every session; label, block count, `*` on the active one. Main tab: **Inspect**.
- **[4] Blocks**: blocks of the selected window, session, or of every session; label, creator, state, last exit, program status, title, `detached`, and `self` on lazyrex's own block. Main tabs: **Capture** and **Inspect** (`block inspect`, `process`, `modes`).
- **[5] Clients**: kind, name, platform, version and connection state. CLI clients (every `rex` call is one) are hidden until you press `A`. Main tabs: **Inspect** (`client inspect`) and **Actions** (`rex -C CLIENT actions --json`, tagged like server actions).
- **Command log**: every command run, with its duration, exit code and full stderr.
- **Footer**: key hints for the focused panel, toasts, and the target's status.

The **Capture** tab runs `rex capture -s SESSION -b BLOCK --format=vt` (falling back to `--format=text` for good if `vt` is rejected), keeps the last `capture.max_lines` lines, and strips every escape sequence except colors. Full-screen programs have no scrollback; the tab then says it shows the current screen.

Updates come from `rex events --all --json`. Events of one session are debounced and trigger at most one `session inspect` at a time, only when the revision is newer. lazyrex's own client events are ignored. When the stream ends, lazyrex reconnects with backoff (1s up to 30s) and polls every `refresh.poll_interval` meanwhile. The Capture tab refreshes every `refresh.capture_interval` and on the block's events while it is visible. The Clients panel refreshes every `refresh.poll_interval` while it is visible. A target's status is read one read at a time; a request meanwhile runs once that read ends.

Every call passes `--autostart=false` and `--timeout`, except `server start`, which runs only when you ask for it.

## Targets

The Targets panel lists, in order: the local server (renamed by a `kind: local` config entry), the `targets` of the config file, the `--server` target when it names none of them or the `--ssh` target when none has its `ssh_host`, then the hosts `rex hosts list --json` discovers on the home (local) server. A discovered host whose label is the server of a config target of the same name adds its details to that row. A host named `local`, one whose name is taken, and one whose label is unsafe as `-S` (empty, a leading `-`, outer whitespace, control characters, over 255 bytes) are hidden, with a line in the command log.

- One target is active: every panel, the event stream and every command use it, through `rex -S SERVER` for a native target and `ssh` for an SSH target (see [SSH targets](#ssh-targets)). `enter` on another row activates it: the previous target's calls and stream are cancelled, its pending results dropped, and the panels re-read. `enter` on the active row re-reads it and the hosts.
- `rex hosts list` and `rex version` always go to the home server. Hosts are re-read at start, on `R`, on `enter` on the active row and after a `hosts_changed` event, never on the poll timer. `rex hosts check --json` runs once after the first list with hosts, and for one host on `c` on its active row; rows show `reachable`, `unreachable`, `down`, or the status Rex gave. The Status tab counts check entries skipped (no label), unrecognized and matching no row.
- When the home server is stopped, the panel says **home server stopped** and hides the discovered hosts, except the active one; they come back when it runs again. A failed list keeps the last good one (**hosts: error**). When Rex reports the home server away, the Status tab says the hosts are as last reached.
- `c` on an inactive row reads its `whoami` and `server status` into that row only, without activating it. `s` and `S` act on the active target only, and server start is never offered for a native target: a stopped server there cannot receive it.
- `a` adds a native or SSH target to the config file. Its menu offers a native target (prefilled from a discovered or `--server` row), each `Host` of `~/.ssh/config` no SSH target uses yet, in file order, and another SSH destination; on an `--ssh` row it adds that one. `e` changes the name and server, or the name, destination and mode, of a config target, `d` removes one. The file keeps its comments and other keys, is replaced atomically, and is refused if it changed on disk since lazyrex loaded it. Discovered hosts, the local target and the active target cannot be edited: lazyrex never changes `rex hosts` or `~/.ssh/config`, and runs no command to edit the config.

### SSH targets

A `kind: ssh` target runs the remote `rex` through `ssh` to `ssh_host`, a `Host` of `~/.ssh/config` or `user@host`. Every `ssh` runs with `BatchMode=yes` and `ConnectTimeout=5`, so it never prompts for a password, passphrase or host key: set up keys, an agent and `known_hosts` first (`ssh HOST` in a terminal). Exec calls share one connection per host (`ControlMaster=auto`, `ControlPersist=60s`), its control socket under `$XDG_RUNTIME_DIR/lazyrex`.

- **Probes**: once per target (and per `rex_path`), lazyrex reads the remote login shell (`echo "$SHELL"`) and, unless `rex_path` is set, looks for `rex` (`command -v rex`, else whichever of `/usr/local/bin/rex`, `/opt/homebrew/bin/rex`, the Rex.app binary and the Rex Beta.app one exists, preferred in that order). The Rex Beta.app path has spaces: it is single-quoted, one word for POSIX shells and fish. Results, failures included, are kept: polls do not probe again, and `c` on the row clears them and probes again.
- **`mode: forward`** (the default): after `whoami` over ssh gives the remote server's endpoint, an `ssh -N` tunnel forwards that Unix socket to `$XDG_RUNTIME_DIR/lazyrex/fwd-PID-N.sock` (that directory is created mode 0700 and must stay yours), and every call runs the local `rex -S unix://…` on it, except `server start`, which goes over exec. A dropped tunnel is restarted with backoff (1s up to 30s, reset once it was up); meanwhile commands are refused, never sent elsewhere. The tunnel is killed and its socket removed when another target is activated and on quit. Only when the endpoint cannot be forwarded (not a `unix://` URI with an absolute path and no query or fragment, a malformed percent escape, or a path that, once percent-decoded, has `:`, `%`, `${`, whitespace or control characters or is over 104 bytes; the default macOS socket under `Application Support` has a space, so a macOS server uses exec mode) does the target fall back to exec mode, with a line in the command log; a forwarded call failing does not.
- **`mode: exec`**: every call runs `ssh HOST REX ARGS…`, each word quoted for the remote shell. It needs a POSIX login shell (sh, bash, zsh, dash, ksh…). With another shell (fish, nushell, or one the probe could not read) exec mode is refused, and forward mode sends over ssh only plain words (letters, digits and `._+@/=:,-`), unquoted.
- **`s`** starts a stopped remote server with `ssh HOST REX server start --json`, after confirming that exact command.
- **`unreachable (ssh)`**: ssh itself failed (exit 255: network, authentication or host key). The Status tab shows its first stderr line and the command log all of it; a mutation failing that way toasts the first line and checks the target again.
- `~/.ssh/config` is read once at start for `a`: only literal `Host` names, without wildcards or negations; `Include` is not followed.

## Actions

- **`x`** opens the action menu of the focused panel: its own keys, by name. The main view and the command log show the menu of the side panel behind them. Targets adds **run a server action on TARGET…** (the active target) and Blocks adds **resize pane…**.
- **Server actions** (Targets `x`) and **client actions** (Clients `a`) are listed fresh from `rex actions --json` or `rex -C CLIENT actions --json`. An action with a JSON Schema in `.args` asks for each argument: required ones first, an enum from a menu, the selected session, window and block prefilled. A server action runs as `rex do NAME --args JSON`, a client action as `rex -C CLIENT do NAME --args JSON`.
- **Resize pane** reads `rex api describe session.resize_pane --json` and builds its form from the input schema, then runs `rex api call -s SESSION session.resize_pane JSON`. If the schema lists no arguments, use the palette.
- **`o`** (Sessions) shows the session in a connected Rex app with `rex -C CLIENT do session.select session_id=SESSION`, after checking that the app offers `session.select`. With several apps, pick one from a menu.

### Command palette

`:` opens a prompt for one of:

```
[rex] api call [-s SESSION | --session SESSION] METHOD [JSON-OBJECT]
[rex] do ACTION [KEY=VALUE ...]
[rex] do ACTION --args JSON-OBJECT
```

The line is split into words without any expansion: `$`, `~`, `*` and backticks stay literal. `'...'` groups text anywhere in a word, literally. `"..."` groups text only at the start of a word, where `\"` and `\\` are its escapes. Elsewhere a backslash only escapes whitespace and `'`, so bare JSON such as `{"a":"x\"y"}` keeps its own escapes. Global flags, `-C`, `@file`, `-` (stdin), `-e` and any other flag are refused, as are Lua script names and anything after the payload.

The output of a palette command goes to the command log. `tab` completes the word under the cursor: `api` and `do`, `call`, method names from `rex api list --quiet`, action names from `rex actions --json`, and anything else from the server's `target.complete` (with the line's `-s SESSION`, if valid). Several candidates open a menu. Method and action names are read once per server instance; denied names are not offered.

## Safety

One policy checks every call, whether it comes from a key, an `x` menu, a form or the palette.

- **Confirmation** shows the exact command before it runs: kill session, close window, close block, stop server, start server, the interrupt keys `ctrl+c`, `ctrl+d`, `ctrl+\` and `ctrl+z`, and any API method, server action or client action whose name closes, kills, destroys, stops or sends input, or that lazyrex does not know.
- **Own block**: when lazyrex runs inside Rex, `$REX_BLOCK` names its own block. Sending text or keys to it, closing it, and killing or closing the session or window that holds it are refused. Only a full block ID (`block:...`) counts; any other value is ignored with a warning in the command log. API calls and actions are refused when an argument names the own block. That includes its ID decoded from JSON escapes, any key or value at any depth, an ID suffix of 4 or more characters, and its current label, followed across refreshes. Arguments that name a block (`block`, `block_id`, `pane`, `pane_id`, `target`, `anchor`, and keys ending in `_block`, `_block_id`, `_pane` or `_pane_id`) must hold a full block ID other than lazyrex's own: a label, suffix or short ID there is refused, even when `$REX_BLOCK` is unset.
- **Denied operations** are refused without a prompt: host (all but the reads `hosts list` and `hosts check`), pairing, listener, Tailscale and update or install operations. `safety.allow_actions` can let a listed update or install action reach a confirmation prompt, but never a host, pairing, listener or Tailscale one.
- **No scripts**: `rex do` runs a name with `/` or `\`, or ending in `.lua`, as a Lua script, and `-` from stdin. Such names are refused everywhere, as are argument keys starting with `@` or `-` and an `--args` that is not a JSON object.
- **Server actions only from the server's list**: a `rex do` from the palette or Targets `x` runs only if a fresh `rex actions --json` lists it. It is also refused when its dotted prefix (`pane.` in `pane.split`) starts an action of a connected Rex app, because `rex do` would send it to that app. That check reads `rex client ls --all --json` and each app's actions fresh, and refuses the call if any read fails. Both checks run before the confirmation and again right before the call, since the lists can change while it waits. Run client actions from Clients `a`, which, like `o`, checks right before the call that the client still offers the action.
- **Native servers**: a `-S` value from the flag, the config or `rex hosts list` stays one argv element, never reaches a shell, and is refused when it is empty, starts with `-`, has outer whitespace or control characters, or is longer than 255 bytes; target names are at most 64 characters. A refused target makes no call.
- **SSH**: every `ssh` runs with `BatchMode=yes` and `--` right before the destination, which is refused when it is empty, starts with `-`, holds whitespace, control characters or invalid UTF-8, or is longer than 255 bytes; so is a `rex_path` that is not an absolute path. Remote words are single-quoted for a POSIX shell, or must be plain words for another one, and are refused otherwise. The forwarded endpoint must be a `unix://` absolute path, forwarded as one `-L` argument to a socket only lazyrex's user can reach; lazyrex opens no TCP listener. A confirmation shows the very argv that then runs: when the route changes in between (tunnel down, mode change), the command is refused instead.
- **Pinned target**: a command runs only on the target and server instance lazyrex last observed when its selection was read: when you pressed the key, or opened the palette or a form that reads Rex first (resize, server and client actions, `o`). A result read after the target changed is dropped. A command is refused, with nothing run, once lazyrex has observed another server instance or the target going offline, or another target activated, since then. That is checked when the form completes and again right before the `rex` process starts, after any confirmation. A restart lazyrex has not observed yet (before the event stream drops or the next poll) is not detected.
- **Child environment**: `REX_CLIENT`, `REX_SESSION` and `REX_BLOCK` are removed from the environment of every `rex` that lazyrex runs, so no call falls back to lazyrex's own pane, session or client. `REX_SERVER` is kept.
- **Redaction**: the text of `send`, and everything after the method or action name of `api call`, `do` and `-C CLIENT do` (JSON, `--args`, and the values of `KEY=VALUE` pairs, whose keys stay visible), is shown as `<redacted N bytes>` in confirmations and in the command log, quoted or not inside an `ssh` argv. Every displayed argv, stderr, title, label and schema text is stripped of escape sequences and control characters.
- Every new pane passes `--focus=false`, so lazyrex never steals focus from a Rex app. Only `f` and `o` change what an app shows.

## Keys

`?` lists the keys of the focused panel, and enter runs the selected one. Override keys in the config under `keybindings.<panel>.<name>`, with the names below; the panel of the first table is `universal`.

### Every panel

| Key | Action | Name |
|---|---|---|
| `q`, `ctrl+c` | quit | `quit` |
| `esc` | close popup, clear filter or search, go back | `back` |
| `?` | keybindings help | `help` |
| `x` | action menu of the focused panel | `menu` |
| `:` | command palette | `palette` |
| `/` | filter the side panel, or search the main view | `filter` |
| `@` | focus the command log | `commandLog` |
| `1`–`5` | jump to a side panel | `jump1`–`jump5` |
| `0` | focus the main view | `jumpMain` |
| `tab`, `l`, right | next panel | `nextPanel` |
| `shift+tab`, `h`, left | previous panel | `prevPanel` |
| `j`, down / `k`, up | move down / up (scroll in the main view and log) | `down` / `up` |
| `<` / `>` | top / bottom | `top` / `bottom` |
| `,` / `.` | page up / down | `pageUp` / `pageDown` |
| `[` / `]` | previous / next main tab | `prevTab` / `nextTab` |
| `pgup`, `ctrl+u` / `pgdn`, `ctrl+d` | scroll the main view a page | `scrollPageUp` / `scrollPageDown` |
| `K` / `J` | scroll the main view a line | `scrollUp` / `scrollDown` |
| `R` | refresh now | `refresh` |

### Per panel

| Panel | Key | Action | Name |
|---|---|---|---|
| `targets` | `enter`, `space` | activate the selected target; on the active one, re-read everything | `activate` |
| | `s` | start server (confirm; active local or SSH target) | `startServer` |
| | `S` | stop server (confirm; active target only) | `stopServer` |
| | `c` | check reachability: re-read the active target, or probe an inactive one; SSH targets are probed again | `checkTarget` |
| | `a` | add a config target, native or SSH | `addTarget` |
| | `e` | edit a config target | `editTarget` |
| | `d` | remove a config target (confirm) | `removeTarget` |
| `sessions` | `enter` | scope Windows and Blocks to it | `scope` |
| | `n` | new session (name, working directory) | `newSession` |
| | `r` | rename | `renameSession` |
| | `d` | kill (confirm) | `killSession` |
| | `o` | show it in a Rex app | `showInApp` |
| `windows` | `enter` | scope Blocks to it | `scope` |
| | `A` | windows of all sessions | `toggleAll` |
| | `f` | focus | `focusWindow` |
| | `n` | new window | `newWindow` |
| | `r` | rename | `renameWindow` |
| | `d` | close (confirm) | `closeWindow` |
| `blocks` | `enter` | show the Capture tab | `showCapture` |
| | `A` | blocks of all sessions | `toggleAll` |
| | `f` | focus | `focusBlock` |
| | `z` | zoom toggle | `toggleZoom` |
| | `n` | new pane: direction, command, label, ratio | `newPane` |
| | `\|` / `-` | split right / below | `splitRight` / `splitBelow` |
| | `i` | send text (no Enter) | `sendText` |
| | `I` | send text, then Enter | `sendTextEnter` |
| | `p` | press a key (`enter`, `tab`, `esc`, arrows, `ctrl+c`, `ctrl+d`, `ctrl+\`, `ctrl+z`, or any key name) | `pressKey` |
| | `r` | rename | `renameBlock` |
| | `d` | close (confirm) | `closeBlock` |
| `clients` | `enter` | show the Inspect tab | `showInspect` |
| | `A` | show CLI clients | `toggleCLI` |
| | `a` | run a client action | `runClientAction` |
| `main` | `n` / `N` | next / previous search match | `nextMatch` / `prevMatch` |

In popups: `enter` accepts and `esc` cancels; `ctrl+c` quits from every popup, `q` from all but text prompts, where it is typed; `j`/`k` move in menus; `y`/`n` answer confirmations; `tab` completes in the palette. A key is never sent to a block unless you pick it with `p`.

The zoom toggle remembers per block whether lazyrex last zoomed it in or out (`rex zoom --on`/`--off`); a zoom changed elsewhere can make the first press a no-op.

## Development

```sh
go test ./...
LAZYREX_SMOKE=1 go test -tags smoke ./pkg/rex/...   # read-only calls against the real rex
```

Tests never run the real `rex` except the opt-in smoke test, which only reads (version, whoami, ls, server status, hosts list) and accepts a stopped server.
