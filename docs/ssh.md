# SSH targets

> **An SSH target reaches Rex on another machine through your own `ssh`: lazyrex probes the remote shell and `rex` once, then either forwards the remote Rex socket through a tunnel (forward mode, the default) or runs the remote `rex` over ssh for every call (exec mode).** ssh never prompts, so keys, agent and `known_hosts` must be ready first.

## Before you start

- A `kind: ssh` target runs the remote `rex` through `ssh` to `ssh_host`, a `Host` of `~/.ssh/config` or `user@host`.
- Every `ssh` runs with `BatchMode=yes` and `ConnectTimeout=5`, so it never prompts for a password, passphrase or host key: set up keys, an agent and `known_hosts` first (`ssh remote-host` in a terminal).
- OpenSSH's `ssh` must be in `PATH`, and `$XDG_RUNTIME_DIR` must be a private directory: a clean absolute path, yours, mode 0700, no symlinks, short enough for socket paths. lazyrex creates `$XDG_RUNTIME_DIR/lazyrex` with mode 0700 for its sockets; it must stay yours. Without it, or on Windows, SSH targets are listed but say why they are not available.

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
    L->>S: ssh -N -L fwd-PID-N.sock:/path/to/rex.sock -- remote-host
    Note over L,S: the local socket lives in XDG_RUNTIME_DIR/lazyrex
    rect rgba(34,197,94,0.15)
    loop every call and the event stream
        L->>R: rex -S unix://fwd-PID-N.sock ARGS, through the tunnel
    end
    end
    rect rgba(239,68,68,0.18)
    S--xL: the tunnel drops
    Note over L: commands refused, restart after 1s, doubling up to 30s
    L->>S: ssh -N again
    end
```

- After `whoami` over ssh gives the remote server's endpoint, an `ssh -N` tunnel forwards that Unix socket to `$XDG_RUNTIME_DIR/lazyrex/fwd-PID-N.sock`, and every call runs the local `rex -S unix://…` on it, except `server start`, which goes over exec.
- A dropped tunnel is restarted with backoff (1s up to 30s, reset once it was up); meanwhile commands are refused, never sent elsewhere. The tunnel is killed and its socket removed when another target is activated and on quit.
- The tunnel runs with `ExitOnForwardFailure=yes`, `StreamLocalBindUnlink=yes`, its own connection (`ControlMaster=no`, `ControlPath=none`) and keepalives (`ServerAliveInterval=15`, `ServerAliveCountMax=3`). lazyrex opens no TCP listener.
- **Fallback**: only when the endpoint cannot be forwarded does the target fall back to exec mode, with a line in the command log; a forwarded call failing does not. An endpoint cannot be forwarded when it is not a `unix://` URI with an absolute path and no query or fragment, has a malformed percent escape, or has a path that, once percent-decoded, holds `:`, `%`, `${`, whitespace or control characters or is over 104 bytes. The default macOS socket under `Application Support` has a space, so a macOS server uses exec mode.

## Exec mode

- Every call runs `ssh remote-host REX ARGS…`. ssh joins those words into one command line for the remote login shell, so lazyrex single-quotes each one.
- Exec calls share one connection per host (`ControlMaster=auto`, `ControlPersist=60s`), its control socket under `$XDG_RUNTIME_DIR/lazyrex`.
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
| a note `...: cannot be forwarded: using exec mode` | the endpoint cannot be forwarded (a macOS socket path with a space, for example) | nothing, or set `mode: exec` to say so |
| `rex not found on remote-host (set rex_path)` | neither `command -v rex` nor a known path found `rex` in the non-interactive login shell | set `rex_path` to its absolute path |
| `exec mode needs a POSIX login shell (sh, bash, zsh...) on remote-host; its shell is fish` | exec mode cannot quote words for that shell | use `mode: forward`, or a POSIX login shell |
| `the login shell on remote-host is not POSIX: lazyrex sends it plain words only` | a word (a `rex_path` with spaces, such as the Rex Beta.app one, or a payload) is not a plain word | make `rex` reachable through `command -v rex` or a path without spaces |
| `SSH targets need a private runtime directory: ...` | `$XDG_RUNTIME_DIR` is unset, not private, too long or goes through a symlink | fix `$XDG_RUNTIME_DIR` and restart lazyrex |
| `ssh tunnel to remote-host is down`, `not connected yet` | a command was refused while the route was not up | wait for the tunnel, then run it again |
| `the route to remote-host changed since this command was prepared; run it again` | the tunnel or the mode changed while a confirmation waited | run it again |

A mutation failing with exit 255 toasts the first stderr line and checks the target again. Probe results are kept, failures included: after fixing something, `c` on the row probes again. More states: [Troubleshooting](troubleshooting.md).
