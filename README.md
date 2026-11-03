# lazyrex

A lazygit-style terminal UI for the Rex terminal multiplexer: every session, window, block and client of the Rex servers you reach, one key away.

lazyrex lists sessions, windows, blocks and clients in side panels, shows the selected item's capture or inspect output in the main view, and writes every `rex` command it runs to a command log. It manages the **local** Rex server, **native hosts** reached through `rex -S SERVER`, and **SSH targets**, Rex servers on other machines reached through `ssh`.

<!-- screenshot: record with vhs or asciinema and replace this comment with ![lazyrex](docs/assets/lazyrex.gif) -->

```mermaid
flowchart LR
    you(["👤 You"]) --> lazyrex["🖥️ lazyrex"]
    lazyrex --> policy{"🛡️ safety policy: every rex command"}
    policy --> cli["rex CLI"]
    policy --> sshexec["ssh"]
    cli -->|"local"| localsrv[("local Rex server")]
    cli -->|"rex -S SERVER"| native[("native host")]
    cli -->|"forward mode: rex -S unix://socket"| tunnel["ssh -N tunnel"]
    tunnel --> remote[("Rex on remote-host")]
    sshexec -->|"exec mode: ssh remote-host rex ..."| remote
    lazyrex -.->|"ssh probes and tunnel: fixed argv, checked apart"| tunnel
    classDef entry fill:#bfdbfe,stroke:#1d4ed8,color:#0f172a
    classDef step fill:#e2e8f0,stroke:#475569,color:#0f172a
    classDef decision fill:#fde68a,stroke:#b45309,color:#1c1917
    classDef caution fill:#fed7aa,stroke:#c2410c,color:#1c1917
    classDef success fill:#bbf7d0,stroke:#15803d,color:#052e16
    classDef failure fill:#fecaca,stroke:#b91c1c,color:#450a0a
    classDef rexside fill:#99f6e4,stroke:#0f766e,color:#042f2e
    class you entry
    class lazyrex,cli,sshexec,tunnel step
    class policy decision
    class localsrv,native,remote rexside
```

## Install

Requires `rex` (API revision 9) in `PATH`. SSH targets also need OpenSSH's `ssh` and lazyrex's private runtime directory (`$XDG_RUNTIME_DIR/lazyrex`, or `/tmp/lazyrex-UID/run` when unset) ([SSH targets](docs/ssh.md)).

On Linux or macOS, amd64 or arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/rbobillot/lazyrex/main/install.sh | sh
```

[The script](install.sh) installs the [latest release](https://github.com/rbobillot/lazyrex/releases/latest) for your OS and architecture in `~/.local/bin`, after checking it against the release's `SHA256SUMS`, and never uses `sudo`. It runs under `bash` too. Set `LAZYREX_VERSION` or `LAZYREX_INSTALL_DIR` to pick another release or directory. To read the script first, or install by hand: [Getting started](docs/getting-started.md#install). Or, with Go 1.26:

```sh
go install github.com/rbobillot/lazyrex/cmd/lazyrex@latest
# or, from a checkout:
go build -o /tmp/lazyrex ./cmd/lazyrex
```

## Quickstart

```sh
lazyrex                          # the local server, config targets and discovered hosts
lazyrex -S studio                # start on a config target, a rex hosts label, host:port or URL
lazyrex --ssh user@remote-host   # start on an SSH target
```

| Key | Does |
|---|---|
| `1`–`5`, `tab` | jump to Targets, Sessions, Windows, Blocks, Clients; next panel |
| `j` / `k`, `enter` | move, then activate, scope or open the selected item |
| `[` / `]` | switch the main view's tab (Capture, Inspect, Status...) |
| `x` | action menu of the focused panel |
| `:` | command palette: `api call` and `do`, with `tab` completion |
| `?` | every key of the focused panel; `enter` runs one |
| `@`, `q` | focus the command log; quit |

[Getting started](docs/getting-started.md) walks through the panels and first actions.

## Documentation

| Page | For |
|---|---|
| [Docs home](docs/index.md) | What to read first |
| [Getting started](docs/getting-started.md) | Install, first run, the panels, moving around |
| [Targets](docs/targets.md) | Local server, native hosts, SSH targets, `--server` and `--ssh` |
| [SSH targets](docs/ssh.md) | Forward and exec modes, probes, fallbacks, the Rex app endpoint, SSH troubleshooting |
| [Keys](docs/keys.md) | Every keybinding and how to override it |
| [Actions](docs/actions.md) | Action menus, forms, server and client actions, the command palette |
| [Safety](docs/safety.md) | What lazyrex refuses, confirms and redacts |
| [Configuration](docs/configuration.md) | Every config key and its default |
| [Troubleshooting](docs/troubleshooting.md) | Target states, errors and exit codes |
| [Architecture](docs/architecture.md) | Packages, transports, events and polling |
| [Development](docs/development.md) | Building, tests, CI and releases, the multi-agent harness |

The same pages are published as the [wiki](https://github.com/rbobillot/lazyrex/wiki), generated from `docs/` by CI: edit `docs/`, never the wiki.

## Safety

lazyrex never starts a server you did not ask for (every other `rex` command passes `--autostart=false`), never changes hosts, pairing, listeners or Tailscale (except the hosts file of this machine's server that `w` on Targets writes after two confirmations), never sends input to or closes the block it runs in, and never steals focus from a Rex app. Destructive commands show the exact command before they run. The command log redacts the text of `send` and the payloads of `api call` and `do`, but shows every other argument as typed, a new pane's command included. Details: [Safety](docs/safety.md).

## Development

```sh
mise exec go@1.26 -- go test ./...
mise exec go@1.26 -- go build -o /tmp/lazyrex ./cmd/lazyrex
```

Agents working here run Go through mise ([AGENTS.md](AGENTS.md)); with Go 1.26 in `PATH`, plain `go test ./...` works too. Tests never run the real `rex` except an opt-in, read-only smoke test. See [Development](docs/development.md).

## Contributing

Fork the repository and open a pull request against `main`: see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0, see [LICENSE](LICENSE).
