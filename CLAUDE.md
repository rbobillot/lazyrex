@AGENTS.md

## Claude Code

- Run every harness role as a native subagent (`Agent` with the `harness-*` `subagent_type` the plan prints). Never call `cursor-agent` or `opencode` from here.
- Subagents are Anthropic models only, named by Claude Code's aliases (`opus`, `sonnet`, `haiku`), so independence is by model: the Implementer is Opus, its reviewers Sonnet or Haiku.
- A session keeps the agent definitions it loaded at its start: after `sync`, restart it (`claude --continue`) before launching a changed agent. Record each launch with `--model` from the subagent's `Model:` line, never from the plan.
- Read-only harness agents have `Read, Grep, Glob` only, no `Bash`: paste the diff (`git diff <base sha>` from `plan`) into their prompt.
- `scripts/harness.sh sync` writes `.claude/agents/` and `.claude/skills/`, which the sandbox protects: run it with the sandbox disabled (the permission prompt asks).
- Use an isolated worktree for the Implementer when another run is open on the same checkout.
