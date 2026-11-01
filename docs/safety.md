# Safety

> **One policy checks every call lazyrex makes, whether it comes from a key, an `x` menu, a form or the palette: some operations are always refused, destructive ones show the exact command first, nothing touches lazyrex's own block, and payloads never reach the command log.** This page lists every rule, so you know what a key can and cannot do before you press it.

## What lazyrex never does

- Start a server you did not ask for: every call passes `--timeout` and `--autostart=false`, except `server start`, which has no `--autostart=false` and runs only when you ask for it and confirm it.
- Change hosts, pairing, listeners or Tailscale settings, by any path.
- Send input to or close the block it runs in, or kill or close the session or window that holds it.
- Steal focus from a Rex app: every new window and pane passes `--focus=false`; only `f` and `o` change what an app shows.
- Run a Lua script, or read a file or stdin, through `rex do`; or hand a local shell anything: every process runs from an argv.
- Edit `rex hosts` or `~/.ssh/config`, or run a command to edit its own config file.

## How a command is classified

```mermaid
flowchart TB
    cmd(["a command"]) --> built{"well formed?"}
    built -->|"no: bad ID, flag-like name, script path"| deny["⛔ refused, nothing runs"]
    built -->|"yes"| route{"target and route pass their checks?"}
    route -->|"no"| deny
    route -->|"yes"| cli{"hosts add, edit or remove, server tailscale, a listener flag?"}
    cli -->|"yes"| deny
    cli -->|"no"| named{"API method, rex do or client action?"}
    named -->|"yes"| classify["classify its name"]
    classify -->|"denied"| deny
    classify -->|"allowed or confirm"| self
    named -->|"no"| self{"names lazyrex's own block?"}
    self -->|"yes"| deny
    self -->|"no"| kind{"what it does"}
    kind -->|"kill, close, server start or stop, interrupt key, name to confirm or unknown"| confirm["⚠️ confirm the exact command"]
    kind -->|"anything else"| allow["✅ allowed"]
    confirm --> again{"same target, route and argv, checked again?"}
    allow --> again
    again -->|"no"| deny
    again -->|"yes"| run["▶️ rex runs, logged with payloads redacted"]
```

Names of API methods, `rex do` actions and client actions are split into words (on `.`, `_`, `-`, `/`, spaces and camelCase, so `checkForUpdates` holds `updates`) and classified:

```mermaid
flowchart LR
    name(["a name"]) --> reads{"an always-allowed read: hosts.list, server.status..."}
    reads -->|"yes"| ok["✅ allowed"]
    reads -->|"no"| hostish{"a host, pairing, listener or Tailscale word?"}
    hostish -->|"yes"| no["⛔ denied"]
    hostish -->|"no"| update{"update or install, not a read?"}
    update -->|"yes, not in safety.allow_actions"| no
    update -->|"no, or allowed"| destructive{"close, kill, destroy, stop, send or input?"}
    destructive -->|"yes"| ask["⚠️ confirm"]
    destructive -->|"no"| known{"known safe?"}
    known -->|"yes"| ok
    known -->|"no"| ask
```

The always-allowed reads are `hosts.list`, `server.status`, `server.version`, `server.whoami` and `server.apple.privacy_status`. The known safe names are `session.view`, `session.list`, `session.list_blocks`, `client.list`, `target.resolve`, `target.complete`, `session.new_split`, `session.new_window`, `session.new_block`, `session.resize_pane`, `session.float_block` and `session.select`. A name made only of `update` or `updates` and the words `status`, `available`, `info`, `get`, `list`, `show` or `version` (such as `updates.status`) is a read, not an update.

The `[allowed]`, `[confirm]` and `[denied]` tags of the Keymap & Actions and client Actions tabs are this classification.

## Confirmations

**Confirmation** shows the exact command before it runs: kill session, close window, close block, stop server, start server, the interrupt keys `ctrl+c`, `ctrl+d`, `ctrl+\` and `ctrl+z`, and any API method, server action or client action whose name closes, kills, destroys, stops or sends input (or is `server.config_reload`), or that lazyrex does not know. `y` or `enter` runs it, `n` or `esc` cancels. Removing a config target is confirmed too.

## Denied operations

- **Denied operations** are refused without a prompt: host (all but the reads `hosts list` and `hosts check`), pairing, listener, Tailscale and update or install operations, `rex hosts add|edit|remove`, `rex server tailscale`, and `rex server start` or `run` with `--extra-listen`, `--allow-anonymous` or `--tailscale`. `server.apple.grant_file_access` is denied too.
- `safety.allow_actions` can let a listed update or install action reach a confirmation prompt, but never a host, pairing, listener or Tailscale one. An entry it can never allow is ignored with a warning at start.
- **No scripts**: `rex do` runs a name with `/` or `\`, or ending in `.lua`, as a Lua script, and `-` from stdin. Such names are refused everywhere, as are argument keys starting with `@` or `-` and an `--args` that is not a JSON object.
- **Server actions only from the server's list**: a `rex do` from the palette or Targets `x` runs only if a fresh `rex actions --json` lists it. It is also refused when its dotted prefix (`pane.` in `pane.split`) starts an action of a connected Rex app, because `rex do` would send it to that app. That check reads `rex client ls --all --json` and each app's actions fresh, and refuses the call if any read fails. Both checks run before the confirmation and again right before the call, since the lists can change while it waits. Run client actions from Clients `a`, which, like `o`, checks right before the call that the client still offers the action.

## Own block

When lazyrex runs inside Rex, `$REX_BLOCK` names its own block, shown as `self` in the Blocks panel.

- Sending text or keys to it, closing it, and killing or closing the session or window that holds it are refused. Only a full block ID (`block:...`) counts; any other value is ignored with a warning in the command log.
- When it knows its own block, lazyrex re-reads `session inspect` (and `block ls` for a session) right before a kill or a window close, and refuses the call when they cannot be read.
- API calls and actions are refused when an argument names the own block. That includes its ID decoded from JSON escapes, any key or value at any depth, an ID suffix of 4 or more characters, and its current label, followed across refreshes.
- Arguments that name a block (`block`, `block_id`, `pane`, `pane_id`, `target`, `anchor`, and keys ending in `_block`, `_block_id`, `_pane` or `_pane_id`) must hold a full block ID other than lazyrex's own: a label, suffix or short ID there is refused, even when `$REX_BLOCK` is unset.

## Targets and transports

- **Native servers**: a `-S` value from the flag, the config or `rex hosts list` stays one argv element, never reaches a shell, and is refused when it is empty, starts with `-`, has outer whitespace or control characters, or is longer than 255 bytes; target names are at most 64 characters. A refused target makes no call.
- **SSH**: every `ssh` runs with `BatchMode=yes` and `--` right before the destination, which is refused when it is empty, starts with `-`, holds whitespace, control characters or invalid UTF-8, or is longer than 255 bytes; so is a `rex_path` that is not an absolute path. Remote words are single-quoted for a POSIX shell, or must be plain words for another one, and are refused otherwise. The forwarded endpoint must be a `unix://` absolute path, forwarded as one `-L` argument to a socket only lazyrex's user can reach; lazyrex opens no TCP listener. A confirmation shows the very argv that then runs: when the route changes in between (tunnel down, mode change), the command is refused instead. Details: [SSH targets](ssh.md).
- **Pinned target**: a command runs only on the target and server instance lazyrex last observed when its selection was read: when you pressed the key, or opened the palette or a form that reads Rex first (resize, server and client actions, `o`). A result read after the target changed is dropped. A command is refused, with nothing run, once lazyrex has observed another server instance or the target going offline, or another target activated, since then. That is checked when the form completes and again right before the `rex` process starts, after any confirmation. A restart lazyrex has not observed yet (before the event stream drops or the next poll) is not detected.

## Processes, logs and screens

- **No shell**: every process runs from an argv, with no shell and no stdin.
- **Child environment**: `REX_CLIENT`, `REX_SESSION` and `REX_BLOCK` are removed from the environment of every `rex` that lazyrex runs, so no call falls back to lazyrex's own pane, session or client. `REX_SERVER` is kept.
- **Redaction**: the text of `send`, and everything after the method or action name of `api call`, `do` and `-C CLIENT do` (JSON, `--args`, and the values of `KEY=VALUE` pairs, whose keys stay visible), is shown as `<redacted N bytes>` in confirmations and in the command log, quoted or not inside an `ssh` argv.
- **Sanitizing**: every displayed argv, stderr, title, label and schema text is stripped of escape sequences and control characters. Captures keep colors only.
- **IDs**: lazyrex names every session, window, block and client by its full ID, never by a label or the current one.

What a refusal looks like and what to do: [Troubleshooting](troubleshooting.md).
