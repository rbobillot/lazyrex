# SSH targets

> **An SSH target reaches Rex on another machine through your own `ssh`: lazyrex probes the remote shell and `rex` once, then either forwards the remote Rex socket through a tunnel (forward mode, the default) or runs the remote `rex` over ssh for every call (exec mode).** ssh never prompts, so keys, agent and `known_hosts` must be ready first.

## Before you start

- A `kind: ssh` target runs the remote `rex` through `ssh` to `ssh_host`, a `Host` of `~/.ssh/config` or `user@host`.
- Every `ssh` runs with `BatchMode=yes` and `ConnectTimeout=5`, so it never prompts for a password, passphrase or host key: set up keys, an agent and `known_hosts` first (`ssh remote-host` in a terminal).
- OpenSSH's `ssh` must be in `PATH`. lazyrex keeps its sockets in a private runtime directory, which must stay yours, mode 0700, with no symlinks:
  - when `$XDG_RUNTIME_DIR` is set, it must be a private directory (a clean absolute path, yours, mode 0700, no symlinks, short enough for socket paths) and lazyrex creates `$XDG_RUNTIME_DIR/lazyrex` in it. A set but invalid value is an error, never a fallback.
  - when it is unset (always on macOS, sometimes on Linux under `su`, `sudo`, cron or containers), lazyrex uses `/tmp/lazyrex-UID/run` (`/private/tmp/…` on macOS), creating both levels with mode 0700. `/tmp` must belong to root or you and be sticky (or closed to others).

  lazyrex checks the directory again, and recreates what is missing, for every SSH target it reads. On Windows, or when the directory fails its checks, SSH targets are listed but say why they are not available.

```yaml
targets:
  - name: edge
    kind: ssh
    ssh_host: user@remote-host
    mode: forward              # the default; or exec
    rex_path: /usr/local/bin/rex   # optional: skips the rex probe
```

Or, without a config entry: `lazyrex --ssh user@remote-host`.

## From probe to mode

```mermaid
flowchart TB
    start(["🔌 read an SSH target"]) --> dir{"private runtime directory?"}
    dir -->|"no"| noDir["error: SSH targets need a private runtime directory"]
    dir -->|"yes"| shell["probe the login shell"]
    shell -->|"exit 255"| sshDown["unreachable (ssh)"]
    shell --> haveRex{"rex_path set?"}
    haveRex -->|"no"| findRex["probe: command -v rex, then the known paths"]
    findRex -->|"exit 255"| sshDown
    findRex -->|"nothing found"| noRex["error: rex not found, set rex_path"]
    findRex --> who
    haveRex -->|"yes"| who["whoami over ssh exec"]
    who -->|"exit 4"| stopped["stopped: s starts it"]
    who -->|"exit 255"| sshDown
    who -->|"ok"| fwdOK{"mode forward, endpoint forwardable?"}
    fwdOK -->|"yes"| fwd["✅ forward mode: ssh -N tunnel, local rex -S unix://socket"]
    fwdOK -->|"no"| posix{"POSIX login shell?"}
    posix -->|"yes"| exec["✅ exec mode: ssh remote-host rex ARGS"]
    posix -->|"no"| noExec["error: exec mode needs a POSIX login shell"]
    classDef entry fill:#bfdbfe,stroke:#1d4ed8,color:#0f172a
    classDef step fill:#e2e8f0,stroke:#475569,color:#0f172a
    classDef decision fill:#fde68a,stroke:#b45309,color:#1c1917
    classDef caution fill:#fed7aa,stroke:#c2410c,color:#1c1917
    classDef success fill:#bbf7d0,stroke:#15803d,color:#052e16
    classDef failure fill:#fecaca,stroke:#b91c1c,color:#450a0a
    classDef rexside fill:#99f6e4,stroke:#0f766e,color:#042f2e
    class start entry
    class dir,haveRex,fwdOK,posix decision
    class shell,findRex,who step
    class stopped caution
    class fwd,exec success
    class noDir,sshDown,noRex,noExec failure
```

**Probes**: once per target (and per `rex_path`), lazyrex reads the remote login shell (`echo "$SHELL"`) and, unless `rex_path` is set, looks for `rex` (`command -v rex`, else whichever of `/usr/local/bin/rex`, `/opt/homebrew/bin/rex`, the Rex.app binary and the Rex Beta.app one exists, preferred in that order). The Rex Beta.app path has spaces: it is single-quoted, one word for POSIX shells and fish. Results, failures included, are kept: polls do not probe again, and `c` on the row clears them and probes again. The probes and the tunnel are `ssh` commands lazyrex builds itself, not `rex` commands: they skip the safety policy and get no `rex` flags, and how they are checked is in [Safety](safety.md).

An SSH row that is not active is read the same way, with `whoami` and `server status` over ssh exec: it never starts a tunnel.

## Forward mode

```mermaid
sequenceDiagram
    autonumber
    box rgba(59,130,246,0.15) local machine
    participant L as lazyrex
    participant S as ssh
    end
    box rgba(20,184,166,0.18) remote-host
    participant R as Rex on remote-host
    end
    L->>S: ssh -- remote-host rex whoami --json
    S->>R: whoami
    R-->>L: endpoint unix:///path/to/rex.sock
    L->>S: ssh -N -L gui-NAME_HASH.sock:/path/to/rex.sock -- remote-host
    Note over L,S: the local socket lives in the private runtime directory
    rect rgba(34,197,94,0.15)
    loop every call and the event stream
        L->>R: rex -S unix://gui-NAME_HASH.sock ARGS, through the tunnel
    end
    end
    rect rgba(239,68,68,0.18)
    S--xL: the tunnel drops
    Note over L: commands refused, restart after 1s, doubling up to 30s
    L->>S: ssh -N again
    end
```

- After `whoami` over ssh gives the remote server's endpoint, an `ssh -N` tunnel forwards that Unix socket to `gui-NAME_HASH.sock` in the runtime directory, and every call runs the local `rex -S unix://…` on it, except `server start`, which goes over exec.
- **Stable socket**: `NAME` is the target name made safe and cut to 14 characters, `HASH` 16 hex digits of the target identity (its name ignoring case, `ssh_host` and `rex_path`), so one target keeps one path across restarts. While a tunnel uses it, lazyrex holds `gui-NAME_HASH.lock` next to it (an exclusive `flock`, gone when lazyrex exits, never deleted). A second lazyrex on the same target, or a live socket already at the path, makes the tunnel use a private `fwd-PID-N.sock` instead. lazyrex removes a socket only when a dial to it is refused; ssh never removes one.
- A dropped tunnel is restarted with backoff (1s up to 30s, reset once it was up); meanwhile commands are refused, never sent elsewhere. The tunnel is killed and its socket removed when another target is activated and on quit.
- The tunnel runs with `ExitOnForwardFailure=yes`, its own connection (`ControlMaster=no`, `ControlPath=none`) and keepalives (`ServerAliveInterval=15`, `ServerAliveCountMax=3`). lazyrex opens no TCP listener.
- **Fallback**: only when the endpoint cannot be forwarded does the target fall back to exec mode, with a line in the command log; a forwarded call failing does not. An endpoint cannot be forwarded when it is not a `unix://` URI with an absolute path and no query or fragment, has a malformed percent escape, or has a path that, once percent-decoded, holds `:`, `%`, `${`, `\`, whitespace other than a space or control characters or is over 104 bytes. A space is kept, so a macOS server, whose socket is under `Application Support`, uses forward mode.

## Using the remote server from the Rex app

> 🖥️ **The short version**: activate the SSH target, then in the Rex app open Settings > Remote Hosts > Add Remote Host with the label and endpoint its Status tab shows, or run once the `rex hosts add` line it shows (lazyrex never runs it). The Rex app then reaches that server while lazyrex runs with the target active.

The Status tab of an active forward-mode target shows:

```text
gui endpoint  unix:///private/tmp/lazyrex-501/run/gui-edge_883cac6c085904fe.sock

Rex app: Settings > Remote Hosts > Add Remote Host, with the label edge and the endpoint unix:///private/tmp/lazyrex-501/run/gui-edge_883cac6c085904fe.sock.
Or run once (lazyrex never runs it):
rex hosts add edge unix:///private/tmp/lazyrex-501/run/gui-edge_883cac6c085904fe.sock
It works while lazyrex runs with edge active. Rex has no transport auth: only your user can open this socket.
Rex syncs hosts to your other devices, where this path reaches nothing unless lazyrex forwards it there.
```

That is target `edge` with `ssh_host: edge` on macOS, where `/tmp` resolves to `/private/tmp`. The `gui endpoint` line is green while the tunnel is up, yellow while it is down or another lazyrex serves the stable socket, and red when there is no endpoint.

- **You add it**: in the Rex app, Settings > Remote Hosts > Add Remote Host; or `rex hosts add` in a terminal. Both store the host through Rex's API. lazyrex refuses every host change ([Safety](safety.md)), so it only shows them.
- **Synced**: a stored host syncs to your other devices, where the path reaches nothing unless lazyrex forwards it there.
- **Lifetime**: the socket exists only while lazyrex runs with the target active; lazyrex installs no service. Renaming the target or changing `ssh_host` or `rex_path` changes the path: change the host in the Rex app.
- **Access**: Rex has no transport authentication on a Unix socket, but the runtime directory is yours alone (mode 0700).
- **Two lazyrex**: the second one says `served by another lazyrex for this same target` and uses a private socket; activate the target again once the first quits to take the stable one over.
- There is no endpoint in exec mode, configured or as a fallback: the Status tab says why.

### Rex app hosts file (experimental)

> 🧪 **Off by default**: as of Rex Beta (November 2026), the Rex app does not show hosts declared in a config file. The server loads them (`rex hosts list` lists `edge-lazyrex` with the source `lazyrex-hosts.lua:4`) and the app reads that list after every reload, but its hosts list leaves them out, with or without an `icon`. Only a stored host (`rex hosts add`) works in the app today.

To try it anyway, set this in lazyrex's config file, then restart lazyrex, which reads the file at start only:

```yaml
experimental:
  rex_hosts_file: true
```

With it on, `w` writes `lazyrex-hosts.lua` in Rex's config directory, with one `rex.host` line per forward-mode SSH target of the config file, after `rex config check` passed on it and you confirmed the path and every line. It then offers to run `rex config reload` on this machine's server, after a second confirmation. lazyrex never edits `init.lua`, so the file declares hosts only once `init.lua` loads it. Until then, the Status tab of an active forward-mode target shows:

```text
gui endpoint  unix:///private/tmp/lazyrex-501/run/gui-edge_883cac6c085904fe.sock

Rex app host edge-lazyrex in /Users/me/.config/rex/lazyrex-hosts.lua: up to date, not loaded yet
If /Users/me/.config/rex/init.lua does not load it yet, add this line there (lazyrex never creates or edits /Users/me/.config/rex/init.lua):
do local f = loadfile("/Users/me/.config/rex/lazyrex-hosts.lua"); if f then f() end end
Then w (Targets) reloads the local Rex server, after a confirmation.
It works while lazyrex runs with edge active. Rex has no transport auth: only your user can open this socket.
```

That is target `edge` with `ssh_host: edge` on macOS, where `/tmp` resolves to `/private/tmp`. Once the server loaded the file, the first line ends in `up to date, loaded by the local Rex server`.

- **Label**: the host is `NAME-lazyrex` (the target name in lowercase letters, digits and `-`, at most 63 bytes), and Targets hides it, with a `hosts: hid "edge-lazyrex": lazyrex's own host` line in the command log. It is not the bare target name because Rex leaves a declared host out when another host already holds its label. A target whose name gives no label, or two targets that would share one, get no host: Status says which to rename.
- **This machine only**: the hosts belong to this machine's server and never sync to your other devices. `rex config check` and `rex config reload` always go to this machine, never through `-S` or ssh.
- **Old `rex hosts add` host**: a host you added with `rex hosts add` for this socket syncs to your other devices, where it reaches nothing. Status names it: remove it in the Rex app. lazyrex never removes hosts. Until the Rex app lists declared hosts, a stored host is the one it uses: keep it.
- **Out of date**: renaming the target or changing `ssh_host` or `rex_path` makes the file out of date: press `w` again.
- **`REX_SERVER`**: while it is set, the file is not available, since lazyrex cannot tell which server reads it. Targets still reads hosts from that server.
- **Two lazyrex** never write the file at once: the second one says `another lazyrex is writing PATH; press w again`.
- The file declares no host for an exec-mode target.

**Turning it off again**: `w` is refused and Status shows the Rex app path and the `rex hosts add` line again. lazyrex no longer reads `lazyrex-hosts.lua`, so a file written meanwhile stays, and this machine's server keeps declaring its hosts while `init.lua` loads it: remove that line from `init.lua`, then the file, by hand. Targets keeps hiding the `NAME-lazyrex` hosts that file declares.

## Exec mode

- Every call runs `ssh remote-host REX ARGS…`. ssh joins those words into one command line for the remote login shell, so lazyrex single-quotes each one.
- Exec calls share one connection per host (`ControlMaster=auto`, `ControlPersist=60s`), its control socket `ssh-%C` in the runtime directory.
- It needs a POSIX login shell (sh, bash, zsh, dash, ksh, ksh93, mksh, ash, yash). With another shell (fish, nushell, or one the probe could not read) exec mode is refused, and forward mode sends over ssh only plain words (letters, digits and `._+@/=:,-`), unquoted.

Forward mode runs your local `rex` against the remote server; exec mode runs the remote machine's own `rex`, with no tunnel.

## Starting a remote server

`s` starts a stopped remote server with `ssh remote-host REX server start --json`, after confirming that exact command. `S` stops it, also after confirmation.

## Troubleshooting

| You see | It means | Do |
|---|---|---|
| `unreachable (ssh)` | ssh itself failed (exit 255: network, authentication or host key). The Status tab shows its first stderr line, the command log all of it | run `ssh remote-host` in a terminal until it logs in without a prompt, then `c` |
| `ssh reaches remote-host, but not its Rex server through the tunnel` | ssh works, but Rex does not answer through the tunnel | check the remote server, then `c` |
| `ssh tunnel: ... (retry in 4s)` | the tunnel process exited; lazyrex retries with backoff | read the line in the command log |
| a note `...: cannot be forwarded: using exec mode` | the endpoint cannot be forwarded (a socket path with `%` or `\`, for example) | nothing, or set `mode: exec` to say so |
| `gui endpoint ... (served by another lazyrex for this same target ...)` | another lazyrex holds the stable socket, or a live socket is at its path | quit the other lazyrex, then activate the target again |
| the tunnel is up but every call fails with `ssh reaches remote-host, but not its Rex server through the tunnel` | the server refuses socket forwarding: `AllowStreamLocalForwarding no` in its `sshd_config`, or `restrict` or `no-port-forwarding` on your key in `authorized_keys` | allow forwarding there, or set `mode: exec` |
| `rex not found on remote-host (set rex_path)` | neither `command -v rex` nor a known path found `rex` in the non-interactive login shell | set `rex_path` to its absolute path |
| `exec mode needs a POSIX login shell (sh, bash, zsh...) on remote-host; its shell is fish` | exec mode cannot quote words for that shell | use `mode: forward`, or a POSIX login shell |
| `the login shell on remote-host is not POSIX: lazyrex sends it plain words only` | a word (a `rex_path` with spaces, such as the Rex Beta.app one, or a payload) is not a plain word | make `rex` reachable through `command -v rex` or a path without spaces |
| `SSH targets need a private runtime directory: XDG_RUNTIME_DIR ...` | `$XDG_RUNTIME_DIR` is set but not private, not yours (`su` keeps the other user's), too long or goes through a symlink | fix or unset `$XDG_RUNTIME_DIR`, then restart lazyrex |
| `SSH targets need a private runtime directory: XDG_RUNTIME_DIR is not set and the fallback in /tmp failed: ...` | `/tmp/lazyrex-UID` or its `run` is not yours, open to others, a symlink or a file (another user may have created it first), or `/tmp` itself is unsafe | ask an administrator to remove `/tmp/lazyrex-UID`, or set `$XDG_RUNTIME_DIR` to a private directory and restart lazyrex. Once it is removed, `c` on the row is enough |
| calls on an active SSH target fail after a tmp cleaner removed `/tmp/lazyrex-UID` | the open target keeps its runtime directory until it is activated again | activate the target again: lazyrex recreates the directory |
| `ssh tunnel to remote-host is down`, `not connected yet` | a command was refused while the route was not up | wait for the tunnel, then run it again |
| `the route to remote-host changed since this command was prepared; run it again` | the tunnel or the mode changed while a confirmation waited | run it again |
| `the Rex app hosts file is not available: experimental.rex_hosts_file is off ...` | `w` with the flag off, the default | run the `rex hosts add` line Status shows, or turn the flag on: [Rex app hosts file](#rex-app-hosts-file-experimental) |
| `rex hosts list` lists `NAME-lazyrex`, the Rex app does not | the Rex app does not show declared hosts (Rex Beta, November 2026) | turn `experimental.rex_hosts_file` off and run the `rex hosts add` line Status shows |
| `PATH is not a file lazyrex wrote (...): lazyrex leaves it alone` | `lazyrex-hosts.lua` exists without lazyrex's header, or is a symlink or not a regular file of yours | move it away, then `w` |
| `PATH not written: rex config check: ...` | this machine's `rex` rejected the file lazyrex would write, or reported anything about it | read the message; nothing was written |
| `Rex app host NAME-lazyrex: the label is taken by another host (...)` | a host stored through the API or declared elsewhere already holds the label, so Rex leaves lazyrex's out | remove or rename that host in the Rex app |
| `up to date, still not loaded after the reload` | the server reloaded, but its hosts list still lacks the host: `init.lua` does not load the file, or this Rex ignores hosts declared in a loaded file | check the line in `init.lua` and the reload output in the command log, or copy the `rex.host` line Status shows into `init.lua` |
| `another lazyrex is writing PATH; press w again` | another lazyrex holds the lock on Rex's config directory | wait, then `w` again |

A mutation failing with exit 255 toasts the first stderr line and checks the target again. Probe results are kept, failures included: after fixing something, `c` on the row probes again. More states: [Troubleshooting](troubleshooting.md).
