# Contributing

Only the owner and invited collaborators can push to this repository. Everyone else forks it, works on a branch of the fork, and opens a pull request against `main`.

## Issues

Report bugs and ask for features in [GitHub issues](https://github.com/rbobillot/lazyrex/issues). For a bug, give the output of `lazyrex --version` and `rex --autostart=false version`, your OS, and the steps that show it. The command log shows most arguments as typed: read it for anything private before you paste it.

## Pull requests

```sh
git clone https://github.com/YOUR-GITHUB-USERNAME/lazyrex.git   # your fork
cd lazyrex
git remote add upstream https://github.com/rbobillot/lazyrex.git
git fetch upstream
git switch -c my-change upstream/main
# edit, then run the checks below
git commit -m "fix: what the change does"
git push -u origin my-change
```

Then open a pull request from that branch of your fork against `main` of `rbobillot/lazyrex`. If `main` moved meanwhile, `git fetch upstream && git rebase upstream/main`, then push again with `--force-with-lease`.

- One change per pull request: a fix, a feature or a docs change, not several.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `test:`...). For a behavior change, write the tests first. See [Contributing](docs/development.md#contributing) in the development guide.
- CI runs on every pull request, but for an outside contributor it waits until someone with write access approves the run.

## Before opening a pull request

Run the checks CI runs, with Go 1.26:

```sh
gofmt -l .            # prints nothing when everything is formatted
go vet ./...
go test ./...
go test -race ./...
```

[Tests](docs/development.md#tests) explains the tests and the opt-in smoke test, and [CI](docs/development.md#ci) lists every check CI runs, including the ones not shown here.

## Tests and the real Rex

Tests never contact a remote host and never run the real `rex` or `ssh`: unit tests drive a fake runner. The only exception is the opt-in smoke test, which only reads metadata from the local Rex. While you work on lazyrex against your own Rex, keep to the habits in [Working against Rex](docs/development.md#working-against-rex). If you use a coding agent, it reads [AGENTS.md](AGENTS.md).

## Docs

Edit `docs/`, never the [wiki](https://github.com/rbobillot/lazyrex/wiki): CI generates it from `docs/` and overwrites any edit made there. `go run ./tools/wikisync` checks the docs the way CI renders them. It reads the committed docs at `HEAD`, not your working tree: commit first, then run it. The rules for links and markup are in [The wiki](docs/development.md#the-wiki).

## Releases

Version tags `v*` are protected by a ruleset that blocks updates and deletions. Only the maintainer tags releases ([Releases](docs/development.md#releases)).

## License

lazyrex is under the Apache License 2.0 ([LICENSE](LICENSE)). Contributions are under the same license.
