#!/usr/bin/env bash
# Hermetic tests of scripts/harness.sh: a copy of the tables and the script in a temporary git repository.
# Run: scripts/test/harness.sh
set -euo pipefail

src="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
fails=0 passes=0

ok() { passes=$((passes + 1)); }
bad() {
  fails=$((fails + 1))
  printf 'FAIL: %s\n' "$*" >&2
}
# expect name code cmd...: the command exits with code; its stdout is in out, its stderr in err.
expect() {
  local name="$1" code="$2" status
  shift 2
  set +e
  out="$("$@" 2>"${work}/err")"
  status=$?
  set -e
  err="$(cat "${work}/err")"
  if [[ "${status}" == "${code}" ]]; then ok; else bad "${name}: exit ${status}, wanted ${code}; stderr: ${err}"; fi
}
contains() { if [[ "$2" == *"$3"* ]]; then ok; else bad "$1: no [$3] in [$2]"; fi; }
lacks() { if [[ "$2" != *"$3"* ]]; then ok; else bad "$1: unexpected [$3] in [$2]"; fi; }

repo="${work}/repo"
mkdir -p "${repo}/scripts"
cp -R "${src}/.agents" "${repo}/.agents"
grep '^#' "${src}/.agents/harness/debt.tsv" >"${repo}/.agents/harness/debt.tsv" || true
cp "${src}/scripts/harness.sh" "${repo}/scripts/harness.sh"
h="${repo}/scripts/harness.sh"
if ! grep -qv '^#' "${repo}/.agents/harness/debt.tsv"; then ok; else bad "the fixture debt.tsv has rows"; fi
printf '.harness/\n' >"${repo}/.gitignore"
mkdir -p "${repo}/src" "${repo}/tests"
printf 'package a\n' >"${repo}/src/a.go"
awk 'BEGIN { for (i = 1; i <= 20; i++) print "line " i }' >"${repo}/tests/a_test.go"
git -C "${repo}" init -q -b main
git -C "${repo}" add -A
git -C "${repo}" -c user.email=t@t -c user.name=t commit -q -m init

# pick: by family for cursor and opencode, by model for claude
expect "pick D2 challengers for cursor" 0 "${h}" pick cursor challenger --count 2 --exclude anthropic
contains "first family" "${out}" $'harness-challenger-gpt-5-6-sol-medium\tgpt-5.6-sol-medium\topenai'
contains "second family" "${out}" $'\tgoogle'
expect "pick D2 challengers for claude" 0 "${h}" pick claude challenger --count 2 --exclude opus
contains "claude first model" "${out}" $'harness-challenger-sonnet\tsonnet\tanthropic'
contains "claude second model" "${out}" $'harness-challenger-haiku\thaiku\tanthropic'
lacks "claude lists no fable" "$(grep -v '^#' "${repo}/.agents/harness/models.tsv" | awk -F '\t' '$1 == "claude"')" fable
expect "claude critic never the implementer's model" 0 "${h}" pick claude critic --exclude opus --skip sonnet
contains "skip picks haiku" "${out}" $'harness-critic-haiku\thaiku'
expect "no model left" 1 "${h}" pick cursor proposer --exclude anthropic
contains "no model left message" "${err}" "cursor/proposer: no model left"
expect "unknown key is usage" 2 "${h}" pick cursor critic --exclude antropic
expect "a family is no claude key" 2 "${h}" pick claude critic --exclude anthropic
expect "opencode handle" 0 "${h}" pick opencode implementer
contains "opencode handle name" "${out}" $'harness-implementer-claude-opus-5-5\t'

# classify
expect "classify docs" 0 "${h}" classify docs/a.md README.md
contains "D0" "${out}" D0
expect "classify code" 0 "${h}" classify src/a.go README.md
contains "D1" "${out}" D1
expect "classify auth" 0 "${h}" classify src/a.go lib/auth/token.go
contains "D2" "${out}" D2
expect "classify harness" 0 "${h}" classify .agents/harness/models.tsv
contains "harness is D2" "${out}" D2
expect "classify nothing" 1 "${h}" classify

# classify: path spellings and agent instruction files cannot lower the class
expect "dot-slash path" 0 "${h}" classify ./scripts/harness.sh
contains "dot-slash is D2" "${out}" D2
expect "absolute path in the repo" 0 "${h}" classify "${repo}/.agents/harness/models.tsv"
contains "absolute is D2" "${out}" D2
expect "double slash" 0 "${h}" classify scripts//harness.sh
contains "double slash is D2" "${out}" D2
expect "path outside the repo" 1 "${h}" classify /etc/passwd
expect "dot-dot path" 1 "${h}" classify src/../scripts/harness.sh
for p in src/AGENTS.md lib/CLAUDE.md tools/skills/x/SKILL.md .mcp.json .cursorrules .githooks/pre-commit \
  .husky/pre-push deploy/server.pem keys/id_ed25519 .npmrc config/aws-credentials.json .gitignore pkg/.gitignore \
  .gitattributes pkg/.gitattributes .gitmodules .vscode/settings.json pkg/.vscode/tasks.json .idea/workspace.xml \
  pkg/.claude/agents/x.md pkg/.cursor/rules/x.mdc pkg/.opencode/agent/x.md pkg/.github/workflows/x.yml \
  CLAUDE.local.md pkg/CLAUDE.local.md opencode.jsonc pkg/opencode.json pkg/opencode.jsonc .cursorignore \
  pkg/.cursorignore .pre-commit-config.yaml pkg/.pre-commit-config.yaml lefthook.yml pkg/lefthook.yml lefthook.yaml \
  pkg/lefthook.yaml .lefthook.yml pkg/.lefthook.yml .lefthook.yaml pkg/.lefthook.yaml .gitlab-ci.yml \
  pkg/.gitlab-ci.yml .gitlab/ci/x.yml pkg/.gitlab/ci/x.yml .circleci/config.yml pkg/.circleci/config.yml Jenkinsfile \
  pkg/Jenkinsfile azure-pipelines.yml pkg/azure-pipelines.yml .travis.yml pkg/.travis.yml bitbucket-pipelines.yml \
  pkg/bitbucket-pipelines.yml .buildkite/pipeline.yml pkg/.buildkite/pipeline.yml .drone.yml pkg/.drone.yml \
  .woodpecker.yml pkg/.woodpecker.yml .woodpecker/build.yml pkg/.woodpecker/build.yml .gitea/workflows/x.yml \
  pkg/.gitea/workflows/x.yml .forgejo/workflows/x.yml pkg/.forgejo/workflows/x.yml appveyor.yml pkg/appveyor.yml \
  cloudbuild.yaml pkg/cloudbuild.yaml .semaphore/semaphore.yml pkg/.semaphore/semaphore.yml .circleci/README.md; do
  expect "classify ${p}" 0 "${h}" classify "${p}"
  contains "${p} is D2" "${out}" D2
done
mkdir -p "${repo}/auth"
printf 'x\n' >"${repo}/"$'auth/\303\251.go'
expect "non-ASCII name in the diff" 0 "${h}" classify --base main
contains "non-ASCII auth path is D2" "${out}" D2
rm -rf "${repo}/auth"

# start: in git a run needs a base
expect "start without base in git" 1 "${h}" start no-base --tool cursor src/a.go
contains "base required" "${err}" "needs --base"

# lite run on cursor: implementer and a critic of another family
expect "start lite" 0 "${h}" start fix-a --tool cursor --base main src/a.go
contains "lite profile" "${out}" "profile=lite"
contains "lite critic not anthropic" "${out}" $'critic\tharness-critic-gpt-5-6-sol-medium'
contains "base pinned" "${out}" "base main pinned at $(git -C "${repo}" rev-parse HEAD)"
expect "run exists" 1 "${h}" start fix-a --tool cursor --base main src/a.go
expect "gate before work" 1 "${h}" gate fix-a
contains "missing implementer" "${err}" "missing stage: implementer"
expect "record implementer" 0 "${h}" record fix-a implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5
expect "critic needs counts" 1 "${h}" record fix-a critic gpt-5.6-sol-medium openai done --model gpt-5.6-sol
expect "unlisted model refused" 1 "${h}" record fix-a critic gpt-5.5-high openai 0,0,0 --model gpt-5.5
contains "debt hint" "${err}" "--debt"
expect "listed slug with the wrong family refused" 1 "${h}" record fix-a critic gpt-5.6-sol-medium anthropic 0,0,0 --model gpt-5.6-sol
expect "record without --model" 1 "${h}" record fix-a critic gpt-5.6-sol-medium openai 0,0,0
contains "model required" "${err}" "record needs --model"
expect "reported id with a space" 1 "${h}" record fix-a critic gpt-5.6-sol-medium openai 0,0,0 --model "gpt 5"
expect "model given twice" 2 "${h}" record fix-a critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol --model gpt-5.6-sol
expect "reported model mismatch refused" 1 "${h}" record fix-a critic gpt-5.6-sol-medium openai 0,0,0 --model claude-opus-5-5
contains "mismatch message" "${err}" "subagent reported claude-opus-5-5, expected *gpt-5?6-sol*: the tool did not run the planned model"
contains "mismatch hint" "${err}" "restart the session after sync"
lacks "mismatch not logged" "$(cat "${repo}/.harness/runs/fix-a/log.tsv")" "0,0,0"
contains "reported model logged" "$(cat "${repo}/.harness/runs/fix-a/log.tsv")" $'1\timplementer\tclaude-opus-5-5-high\tanthropic\tdone\t\tclaude-opus-5-5'
expect "spelling and case tolerated" 0 "${h}" record fix-a critic gpt-5.6-sol-medium openai 0,0,0 --model GPT-5-6-Sol
contains "record prints the reported model" "${out}" "reported GPT-5-6-Sol"
expect "same-family critic recorded" 0 "${h}" record fix-a critic claude-opus-5-5-high anthropic 0,1,0 --model claude-opus-5-5
expect "gate refuses same family" 1 "${h}" gate fix-a
contains "family message" "${err}" "critic of the Implementer's family anthropic"

# D1 run on claude with a base: model independence, blockers, rejection, rounds, class rising with the diff
expect "start D1" 0 "${h}" start feat-b --tool claude --base main src/a.go src/b.go
contains "D1 profile" "${out}" "profile=D1"
contains "independent by model" "${out}" "independent by model"
contains "D1 challenger sonnet" "${out}" $'challenger\tharness-challenger-sonnet'
contains "D1 critic sonnet" "${out}" $'critic\tharness-critic-sonnet'
"${h}" record feat-b proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record feat-b challenger sonnet anthropic 1,2,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record feat-b implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record feat-b critic sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
expect "sonnet launch that ran opus refused" 1 "${h}" record feat-b critic sonnet anthropic 0,0,0 --model claude-opus-5-5
contains "sonnet mismatch" "${err}" "subagent reported claude-opus-5-5, expected claude-sonnet-*"
expect "open blocker fails" 1 "${h}" gate feat-b
contains "open critic blocker" "${err}" "critic: 1 open BLOCKER"
contains "open challenger blocker" "${err}" "challenger: 1 open BLOCKER"
expect "reject needs file:line" 1 "${h}" reject feat-b critic sonnet "trust me"
expect "reject needs a reason" 1 "${h}" reject feat-b critic sonnet "tests/a_test.go:12"
expect "reject of a missing file" 1 "${h}" reject feat-b critic sonnet "nonexistent.go:1 because"
expect "reject past the last line" 1 "${h}" reject feat-b critic sonnet "tests/a_test.go:21 because"
expect "reject with an overflowing line" 1 "${h}" reject feat-b critic sonnet "tests/a_test.go:18446744073709551617 because"
expect "reject with a ten-digit line" 1 "${h}" reject feat-b critic sonnet "tests/a_test.go:1000000001 because"
expect "reject citing the run log" 1 "${h}" reject feat-b critic sonnet ".harness/runs/feat-b/log.tsv:1 because"
expect "reject with a control character" 1 "${h}" reject feat-b critic sonnet $'tests/a_test.go:12 a\033[2Jb'
expect "reject with proof" 0 "${h}" reject feat-b critic sonnet "./tests/a_test.go:12 covers the empty input"
contains "none left" "${out}" "0 left open"
expect "no blocker left to reject" 1 "${h}" reject feat-b critic sonnet "tests/a_test.go:12 again"
expect "challenger blocker still open" 1 "${h}" gate feat-b
contains "challenger open" "${err}" "challenger: 1 open BLOCKER"
expect "reject challenger blocker" 0 "${h}" reject feat-b challenger sonnet "src/a.go:1 the plan keeps the package"
expect "gate passes" 0 "${h}" gate feat-b
contains "pass line" "${out}" "gate feat-b: pass (class D1"
"${h}" record feat-b critic sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record feat-b critic sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "third round fails" 1 "${h}" gate feat-b
contains "rounds" "${err}" "3 rounds"
mkdir -p "${repo}/src/auth"
printf 'x\n' >"${repo}/src/auth/t.go"
expect "class rises with the diff" 1 "${h}" gate feat-b
contains "profile D2" "${err}" "profile D2"
contains "security critic missing" "${err}" "missing stage: security-critic"
expect "plan after the rise" 0 "${h}" plan feat-b
contains "security critic avoids implementer and critic" "${out}" $'security-critic\tharness-security-critic-haiku'
lacks "no restart for a stage after the implementer" "${out}" "start a new run"
rm -rf "${repo}/src/auth"

# claude: a critic on the Implementer's model is refused even though every model is anthropic
expect "start same-model" 0 "${h}" start same-c --tool claude --base main src/a.go
"${h}" record same-c implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record same-c critic opus anthropic 0,0,0 --model claude-opus-5-5 --debt "test" >/dev/null
expect "gate refuses same model" 1 "${h}" gate same-c
contains "model message" "${err}" "critic of the Implementer's model opus"
expect "debt with a control character" 1 "${h}" record same-c critic opus anthropic 0,0,0 --model claude-opus-5-5 --debt $'a\rb'
expect "run id taken" 1 "${h}" start same-c --tool claude --base main src/a.go
rm -rf "${repo}/.harness/runs/same-c"
expect "run id with debt cannot be reused after removal" 1 "${h}" start same-c --tool claude --base main src/a.go
contains "debt id message" "${err}" "has model debt"

# debt.tsv is bookkeeping: writing it never raises a run's class
expect "start debt run" 0 "${h}" start debt-e --tool claude --base main src/a.go
"${h}" record debt-e implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record debt-e critic claude-sonnet-4 anthropic 0,0,0 --model claude-sonnet-4-5 --debt "sonnet quota" >/dev/null
expect "debt keeps the profile" 0 "${h}" gate debt-e
contains "still lite" "${out}" "profile lite"
contains "pass with debt" "${out}" "pass with model debt"

# a mismatch accepted by the human: --debt still needs the reported id, logs it and writes it to debt.tsv
expect "start mismatch run" 0 "${h}" start mis-m --tool claude --base main src/a.go
"${h}" record mis-m implementer opus anthropic done --model claude-opus-5-5 >/dev/null
expect "debt still needs --model" 1 "${h}" record mis-m critic haiku anthropic 0,0,0 --debt "stale agent"
expect "debt with a mismatch allowed" 0 "${h}" record mis-m critic haiku anthropic 0,0,0 --debt "stale agent" --model claude-sonnet-5-5
contains "debt mismatch logged" "$(cat "${repo}/.harness/runs/mis-m/log.tsv")" $'\tdebt: stale agent\tclaude-sonnet-5-5'
contains "debt row with reported id" "$(cat "${repo}/.agents/harness/debt.tsv")" $'mis-m\tclaude\tcritic\thaiku\tanthropic\tstale agent; reported claude-sonnet-5-5'
expect "mismatch with debt passes with debt" 0 "${h}" gate mis-m
contains "mismatch debt pass" "${out}" "pass with model debt"
grep -v $'\tmis-m\t' "${repo}/.agents/harness/debt.tsv" >"${work}/debt" || true
cp "${work}/debt" "${repo}/.agents/harness/debt.tsv"
expect "own debt rows removed" 1 "${h}" gate mis-m
contains "removed rows message" "${err}" "debt.tsv has 0 row(s) for run mis-m, its log 1 --debt launch(es)"

# independence holds against every launch of the Implementer and of the Proposer, not the last one
expect "start keys run" 0 "${h}" start keys-n --tool claude --base main src/a.go src/b.go
"${h}" record keys-n proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record keys-n proposer sonnet anthropic done --model claude-sonnet-5-5 --debt "test" >/dev/null
"${h}" record keys-n challenger opus anthropic 0,0,0 --model claude-opus-5-5 --debt "test" >/dev/null
"${h}" record keys-n implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record keys-n critic haiku anthropic 0,0,0 --model claude-haiku-5-5 >/dev/null
"${h}" record keys-n implementer sonnet anthropic done --model claude-sonnet-5-5 --debt "test" >/dev/null
"${h}" record keys-n critic opus anthropic 0,0,0 --model claude-opus-5-5 --debt "test" >/dev/null
expect "earlier keys count" 1 "${h}" gate keys-n
contains "earlier proposer" "${err}" "challenger of the Proposer's model opus"
contains "earlier implementer" "${err}" "critic of the Implementer's model opus"
expect "plan excludes every implementer key" 0 "${h}" plan keys-n
contains "critic avoids opus and sonnet" "${out}" $'critic\tharness-critic-haiku'

# independence of a --debt launch goes by the model it reported, not by the slug it was recorded under
expect "start debt key run" 0 "${h}" start dkey-x --tool claude --base main src/a.go src/b.go
"${h}" record dkey-x proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record dkey-x challenger sonnet anthropic 0,0,0 --model claude-opus-5-5 --debt "test" >/dev/null
expect "debt slug does not hide the reported model" 1 "${h}" gate dkey-x
contains "reported model key" "${err}" "challenger of the Proposer's model opus"

# rejects close the BLOCKERs of rounds before them, never those of a later round
expect "start reject order run" 0 "${h}" start chal-v --tool claude --base main src/auth/x.go
"${h}" record chal-v proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record chal-v challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record chal-v challenger haiku anthropic 1,0,0 --model claude-haiku-5-5 >/dev/null
expect "reject the first challenger blocker" 0 "${h}" reject chal-v challenger sonnet "src/a.go:1 the plan keeps the package"
expect "reject the second challenger blocker" 0 "${h}" reject chal-v challenger haiku "src/a.go:1 the plan keeps the package"
contains "both closed" "${out}" "0 left open"
expect "gate after both rejects" 1 "${h}" gate chal-v
lacks "rejects after both rounds close both" "${err}" "open BLOCKER"
"${h}" record chal-v challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
expect "a later round's blocker stays open" 1 "${h}" gate chal-v
contains "later blocker open" "${err}" "challenger: 1 open BLOCKER"
expect "the later blocker can be rejected" 0 "${h}" reject chal-v challenger sonnet "src/a.go:1 the plan keeps the package"
contains "later blocker closed" "${out}" "0 left open"

# a critic BLOCKER closes by a reject, or by a later round of the same model after a writing launch
expect "start no-fix critic run" 0 "${h}" start b2-a --tool cursor --base main src/a.go
"${h}" record b2-a implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record b2-a critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record b2-a critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "a critic round with no fix before it closes nothing" 1 "${h}" gate b2-a
contains "no-fix critic blocker open" "${err}" "critic: 1 open BLOCKER"
expect "a reject closes the no-fix blocker" 0 "${h}" reject b2-a critic gpt-5.6-sol-medium "src/a.go:1 the package is kept"
contains "no-fix blocker rejected" "${out}" "0 left open"
expect "gate after the no-fix reject" 0 "${h}" gate b2-a
expect "start mechanical fix run" 0 "${h}" start b2-b --tool cursor --base main src/a.go
"${h}" record b2-b implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record b2-b critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record b2-b mechanical composer-2.5-fast cursor done --model composer-2.5 >/dev/null
"${h}" record b2-b critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "a mechanical launch counts as a fix" 0 "${h}" gate b2-b
expect "start added blockers run" 0 "${h}" start b2-c --tool cursor --base main src/a.go
"${h}" record b2-c implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record b2-c critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record b2-c critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
expect "rounds with no fix between add up" 1 "${h}" gate b2-c
contains "added blockers" "${err}" "critic: 2 open BLOCKER"
expect "a run with open BLOCKERs cannot be replaced" 1 "${h}" start rep-b --tool cursor --replaces b2-c
contains "open blockers message" "${err}" "run b2-c has 2 open critic BLOCKER(s): reject or fix them in run b2-c first"
if [[ ! -e "${repo}/.harness/runs/rep-b" ]]; then ok; else bad "a refused replacing run was created"; fi

# a --debt substitute recorded under a slug that already has a round would close that slug's BLOCKER
expect "start same-slug substitute run" 0 "${h}" start dsub-a --tool claude --base main src/a.go
"${h}" record dsub-a implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record dsub-a critic sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record dsub-a implementer opus anthropic done --model claude-opus-5-5 >/dev/null
cp "${repo}/.agents/harness/debt.tsv" "${work}/debt-before-dsub"
expect "a substitute under another model's slug" 1 "${h}" record dsub-a critic sonnet anthropic 0,0,0 --model claude-haiku-5-5 --debt "quota"
contains "own slug message" "${err}" "record the substitute under its own slug"
if cmp -s "${work}/debt-before-dsub" "${repo}/.agents/harness/debt.tsv"; then ok; else bad "a refused substitute wrote a debt row"; fi
expect "a substitute under its own slug" 0 "${h}" record dsub-a critic haiku anthropic 0,0,0 --model claude-haiku-5-5 --debt "quota"
contains "substitute numbered" "${out}" "dsub-a #4"
expect "the substitute closes nothing" 1 "${h}" gate dsub-a
contains "substitute leaves sonnet's blocker" "${err}" "critic: 1 open BLOCKER"
expect "start no-fix security run" 0 "${h}" start b2-d --tool opencode --base main src/auth/x.go
contains "no-fix security run is D2" "${out}" "profile=D2"
"${h}" record b2-d proposer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record b2-d challenger github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record b2-d challenger github-copilot/gemini-4-argon google 0,0,0 --model gemini-4-argon >/dev/null
"${h}" record b2-d implementer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record b2-d critic github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record b2-d security-critic github-copilot/gemini-4-argon google 1,0,0 --model gemini-4-argon >/dev/null
"${h}" record b2-d security-critic github-copilot/gemini-4-argon google 0,0,0 --model gemini-4-argon >/dev/null
expect "a security round with no fix before it closes nothing" 1 "${h}" gate b2-d
contains "no-fix security blocker open" "${err}" "security-critic: 1 open BLOCKER"

# critic BLOCKERs are kept per model: another model's round or reject never closes them
expect "start substitute critic run" 0 "${h}" start sub-a --tool claude --base main src/a.go
"${h}" record sub-a implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record sub-a critic sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record sub-a critic haiku anthropic 0,0,0 --model claude-haiku-5-5 >/dev/null
expect "another critic model closes nothing" 1 "${h}" gate sub-a
contains "substitute leaves the blocker" "${err}" "critic: 1 open BLOCKER"
expect "a reject must name the model with the blocker" 1 "${h}" reject sub-a critic haiku "src/a.go:1 the package is kept"
contains "wrong model message" "${err}" "no open BLOCKER to reject for critic haiku"
expect "reject naming the model with the blocker" 0 "${h}" reject sub-a critic sonnet "src/a.go:1 the package is kept"
contains "critic reject logs its slug" "$(cat "${repo}/.harness/runs/sub-a/log.tsv")" $'4\tcritic\tsonnet\t-\treject\tsrc/a.go:1 the package is kept\t-'
expect "gate after the named reject" 0 "${h}" gate sub-a
expect "start substitute after a fix run" 0 "${h}" start sub-b --tool claude --base main src/a.go
"${h}" record sub-b implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record sub-b critic sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record sub-b implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record sub-b critic haiku anthropic 0,0,0 --model claude-haiku-5-5 >/dev/null
expect "another critic model after a fix closes nothing" 1 "${h}" gate sub-b
contains "substitute after a fix" "${err}" "critic: 1 open BLOCKER"
expect "start substitute security run" 0 "${h}" start sub-c --tool opencode --base main src/auth/x.go
"${h}" record sub-c proposer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sub-c challenger github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record sub-c challenger github-copilot/gemini-4-argon google 0,0,0 --model gemini-4-argon >/dev/null
"${h}" record sub-c implementer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sub-c critic github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record sub-c security-critic github-copilot/gemini-4-argon google 1,0,0 --model gemini-4-argon >/dev/null
"${h}" record sub-c implementer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sub-c critic github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record sub-c security-critic github-copilot/grok-4.7 xai 0,0,0 --model grok-4.7 >/dev/null
expect "another security model after a fix closes nothing" 1 "${h}" gate sub-c
contains "security substitute after a fix" "${err}" "security-critic: 1 open BLOCKER"
expect "a security reject must name the model with the blocker" 1 "${h}" reject sub-c security-critic github-copilot/grok-4.7 "src/a.go:1 the package is kept"
contains "wrong security model message" "${err}" "no open BLOCKER to reject for security-critic github-copilot/grok-4.7"
expect "security reject naming the model" 0 "${h}" reject sub-c security-critic github-copilot/gemini-4-argon "src/a.go:1 the package is kept"
expect "gate after the named security reject" 0 "${h}" gate sub-c

# a Challenger BLOCKER closes by a reject naming its slug, or by that slug's round after a Proposer launch
expect "start no-replan run" 0 "${h}" start b2-e --tool claude --base main src/a.go src/b.go
contains "no-replan run is D1" "${out}" "profile=D1"
"${h}" record b2-e proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record b2-e challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record b2-e challenger sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "a challenger re-run without a re-plan closes nothing" 1 "${h}" gate b2-e
contains "no-replan blocker open" "${err}" "challenger: 1 open BLOCKER"
expect "start named reject run" 0 "${h}" start b3-a --tool claude --base main src/a.go src/b.go
contains "named reject run is D1" "${out}" "profile=D1"
"${h}" record b3-a proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record b3-a challenger haiku anthropic 1,0,0 --model claude-haiku-5-5 >/dev/null
"${h}" record b3-a challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
expect "reject sonnet's blocker" 0 "${h}" reject b3-a challenger sonnet "src/a.go:1 the plan keeps the package"
contains "haiku's still open" "${out}" "1 left open"
"${h}" record b3-a challenger sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "a reject never closes another slug's blocker" 1 "${h}" gate b3-a
contains "haiku blocker open" "${err}" "challenger: 1 open BLOCKER"
expect "sonnet has none left" 1 "${h}" reject b3-a challenger sonnet "src/a.go:1 the plan keeps the package"
contains "sonnet none left message" "${err}" "no open BLOCKER to reject for challenger sonnet"
expect "reject of a slug with no round" 1 "${h}" reject b3-a challenger opus "src/a.go:1 the plan keeps the package"
contains "no round message" "${err}" "no challenger round recorded as opus"
expect "reject without a slug is usage" 2 "${h}" reject b3-a challenger "src/a.go:1 the plan keeps the package"
expect "reject haiku's blocker" 0 "${h}" reject b3-a challenger haiku "src/a.go:1 the plan keeps the package"
contains "all closed" "${out}" "0 left open"
contains "reject logs its slug" "$(sed -n 6p "${repo}/.harness/runs/b3-a/log.tsv")" $'6\tchallenger\thaiku\t-\treject\t'

# a reject written before slugs (field 3 `-`) closes nothing: reject again naming the slug
expect "start migration run" 0 "${h}" start mig-x --tool claude --base main src/a.go
"${h}" record mig-x implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record mig-x critic sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
printf '3\tcritic\t-\t-\treject\tsrc/a.go:1 old syntax\t-\n' >>"${repo}/.harness/runs/mig-x/log.tsv"
expect "an old reject closes nothing" 1 "${h}" gate mig-x
contains "old reject leaves it open" "${err}" "critic: 1 open BLOCKER"
expect "reject again with the slug" 0 "${h}" reject mig-x critic sonnet "src/a.go:1 the package is kept"
contains "migrated reject numbered" "${out}" "mig-x #4"
expect "gate after the migrated reject" 0 "${h}" gate mig-x

# a revised plan is challenged again: only Challengers after the Proposer's last launch count
expect "start replan run" 0 "${h}" start replan-w --tool claude --base main src/a.go src/b.go
"${h}" record replan-w proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-w challenger sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record replan-w proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-w implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-w critic sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "unchallenged revised plan" 1 "${h}" gate replan-w
contains "revised plan message" "${err}" "challengers: 1 of distinct model needed, got [] after the last proposer launch (#3)"
expect "start rechallenge run" 0 "${h}" start replan-y --tool claude --base main src/a.go src/b.go
"${h}" record replan-y proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-y challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record replan-y proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-y challenger haiku anthropic 0,0,0 --model claude-haiku-5-5 >/dev/null
"${h}" record replan-y implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-y critic sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "a re-plan does not close the old plan's blocker" 1 "${h}" gate replan-y
contains "old blocker still open" "${err}" "challenger: 1 open BLOCKER"
lacks "rechallenged plan has its challenger" "${err}" "challengers:"
expect "start same-slug rechallenge run" 0 "${h}" start replan-z --tool claude --base main src/a.go src/b.go
"${h}" record replan-z proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-z challenger sonnet anthropic 1,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record replan-z proposer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-z challenger sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
"${h}" record replan-z implementer opus anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record replan-z critic sonnet anthropic 0,0,0 --model claude-sonnet-5-5 >/dev/null
expect "by design: the same challenger re-run after a Proposer launch closes its blocker" 0 "${h}" gate replan-z

# a --debt launch whose reported id matches no row keeps its recorded key
expect "start unlisted substitute run" 0 "${h}" start unl-a --tool cursor --base main src/a.go
"${h}" record unl-a implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record unl-a critic claude-sonnet-4 anthropic 0,0,0 --model claude-sonnet-4-5 --debt "test" >/dev/null
expect "unlisted substitute of the implementer's family" 1 "${h}" gate unl-a
contains "unlisted family message" "${err}" "critic of the Implementer's family anthropic"
expect "start unlisted challengers run" 0 "${h}" start unl-b --tool cursor --base main src/auth/x.go
"${h}" record unl-b proposer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record unl-b challenger gpt-7-a openai 0,0,0 --model gpt-7-a --debt "test" >/dev/null
"${h}" record unl-b challenger gpt-7-b openai 0,0,0 --model gpt-7-b --debt "test" >/dev/null
expect "two unlisted models of one family" 1 "${h}" gate unl-b
contains "one family counted once" "${err}" "challengers: 2 of distinct family needed, got [openai]"
expect "a dash is no reported model" 1 "${h}" record unl-b critic gpt-7-c openai 0,0,0 --model - --debt "test"
contains "dash message" "${err}" "--model"
expect "start listed substitute run" 0 "${h}" start lst-a --tool cursor --base main src/a.go
"${h}" record lst-a implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record lst-a critic gpt-5.6-sol-medium openai 0,0,0 --model claude-opus-5-5 --debt "test" >/dev/null
expect "a listed id takes the key of the row it matches" 1 "${h}" gate lst-a
contains "listed id keyed by its row" "${err}" "critic of the Implementer's family anthropic"

# record and reject check the log numbering after their append: a number taken twice is reported, never hidden
expect "start numbering run" 0 "${h}" start seq-r --tool cursor --base main src/a.go
"${h}" record seq-r implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
printf '1\tcritic\tgpt-5.6-sol-medium\topenai\t0,0,0\t\tgpt-5.6-sol\n' >>"${repo}/.harness/runs/seq-r/log.tsv"
expect "record after a duplicate number" 1 "${h}" record seq-r critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol
contains "record numbering message" "${err}" "#3 written, but log.tsv line 2 is out of sequence"
contains "record line written" "$(sed -n 3p "${repo}/.harness/runs/seq-r/log.tsv")" $'3\tcritic'
expect "start reject numbering run" 0 "${h}" start seq-s --tool cursor --base main src/a.go
"${h}" record seq-s implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record seq-s critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
printf '2\tcritic\tgpt-5.6-sol-medium\topenai\t1,0,0\t\tgpt-5.6-sol\n' >>"${repo}/.harness/runs/seq-s/log.tsv"
expect "reject after a duplicate number" 1 "${h}" reject seq-s critic gpt-5.6-sol-medium "src/a.go:1 the package is kept"
contains "reject numbering message" "${err}" "#4 written, but log.tsv line 3 is out of sequence"

# order: a review precedes the code it reviews, or code follows the last review
expect "start order run" 0 "${h}" start order-f --tool cursor --base main src/a.go
"${h}" record order-f critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record order-f implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
expect "critic before implementer" 1 "${h}" gate order-f
contains "order message" "${err}" "order: the last critic round (#1) precedes write launch #2"
"${h}" record order-f critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "critic after implementer" 0 "${h}" gate order-f
"${h}" record order-f mechanical composer-2.5-fast cursor done --model composer-2.5 >/dev/null
expect "write after the last review" 1 "${h}" gate order-f
contains "mechanical after review" "${err}" "precedes write launch #4"
lacks "four launches fit lite" "${err}" "launches:"
"${h}" record order-f explorer composer-2.5-fast cursor done --model composer-2.5 >/dev/null
expect "fifth launch" 1 "${h}" gate order-f
contains "budget overflow" "${err}" "launches: 5, budget 4"

# lite fits one fix loop: implementer, critic, implementer again, critic
expect "start fix loop run" 0 "${h}" start loop-u --tool cursor --base main src/a.go
"${h}" record loop-u implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record loop-u critic gpt-5.6-sol-medium openai 1,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record loop-u implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record loop-u critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "one fix loop fits lite" 0 "${h}" gate loop-u
contains "four launches" "${out}" "4 launches"

# record and reject take a lock on the run: a held lock is waited for, a stale one is taken over
expect "start lock run" 0 "${h}" start lock-l --tool cursor --base main src/a.go
lk="${repo}/.harness/runs/lock-l/lock"
llog="${repo}/.harness/runs/lock-l/log.tsv"
mkdir "${lk}"
printf '%s\n' "$$" >"${lk}/pid"
"${h}" record lock-l implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >"${work}/lock-out" 2>&1 &
bg=$!
sleep 1
if [[ ! -s "${llog}" ]]; then ok; else bad "record wrote while the lock was held"; fi
rm "${lk}/pid"
rmdir "${lk}"
if wait "${bg}"; then ok; else bad "record after the release: $(cat "${work}/lock-out")"; fi
contains "line after the release" "$(cat "${llog}")" $'1\timplementer'
if [[ ! -e "${lk}" ]]; then ok; else bad "lock left after record"; fi
mkdir "${lk}"
expect "lock without pid taken over" 0 "${h}" record lock-l critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol
contains "numbered after the no-pid takeover" "${out}" "lock-l #2"
if [[ ! -e "${lk}" ]]; then ok; else bad "no-pid lock left"; fi
mkdir "${lk}"
printf '%s\n' "$$" >"${lk}/pid"
touch -t 200001010000 "${lk}/pid" "${lk}"
expect "old lock taken over" 0 "${h}" record lock-l critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol
contains "numbered after the old-lock takeover" "${out}" "lock-l #3"
if [[ ! -e "${lk}" ]]; then ok; else bad "old lock left"; fi
expect "refused reject" 1 "${h}" reject lock-l critic gpt-5.6-sol-medium "src/a.go:1 no blocker to reject"
contains "refused under the lock" "${err}" "no open BLOCKER"
if [[ ! -e "${lk}" ]]; then ok; else bad "a refused reject left the lock"; fi
lacks "no takeover leftovers" "$(ls -a "${repo}/.harness/runs/lock-l")" "lock"
mkdir "${lk}"
printf '%s\n' "$$" >"${lk}/pid"
touch -t 200001010000 "${lk}"
cp "${repo}/.agents/harness/debt.tsv" "${work}/debt-before-lock"
expect "a live pid file keeps an old lock" 1 "${h}" record lock-l explorer composer-2.5-fast cursor done --model composer-2.5 --debt "test"
contains "live lock message" "${err}" "locked by process $$"
if [[ -f "${lk}/pid" && "$(cat "${lk}/pid")" == "$$" ]]; then ok; else bad "a live lock was taken over"; fi
if cmp -s "${work}/debt-before-lock" "${repo}/.agents/harness/debt.tsv"; then ok; else bad "debt row written without the lock"; fi
if [[ "$(grep -c . "${llog}")" == 3 ]]; then ok; else bad "logged without the lock: $(grep -c . "${llog}") lines, wanted 3"; fi
rm -f "${lk}/pid"
if [[ -d "${lk}" ]]; then rmdir "${lk}"; fi
lacks "no aside left by a put-back" "$(ls -a "${repo}/.harness/runs/lock-l")" "lock"
for i in 1 2 3 4 5 6; do
  "${h}" record lock-l explorer composer-2.5-fast cursor done --model composer-2.5 >/dev/null 2>&1 &
done
wait
if [[ "$(grep -c . "${llog}")" == 9 ]]; then ok; else bad "parallel records: $(grep -c . "${llog}") lines, wanted 9"; fi
lacks "pid file written by the holder only" "$(ls -a "${repo}/.harness/runs/lock-l")" "stale"
contains "parallel records in sequence" "[$(awk -F '\t' '$1 != NR' "${llog}")]" "[]"

# a stage required before the Implementer, added by a rise after it ran, needs a new run; before it, the stage
expect "start rise run" 0 "${h}" start rise-t --tool cursor --base main src/a.go
expect "start rise run without implementer" 0 "${h}" start rise-u --tool cursor --base main src/a.go
"${h}" record rise-t implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
printf 'package c\n' >"${repo}/src/c.go"
printf 'package d\n' >"${repo}/src/d.go"
expect "rise after the implementer" 1 "${h}" gate rise-t
contains "new run message" "${err}" "order: proposer is required before the implementer, which already ran (#1): start a new run"
contains "new run replaces this one" "${err}" "start a new run with --replaces rise-t"
expect "plan after the rise" 0 "${h}" plan rise-t
contains "profile rose" "${out}" "profile rose from lite"
contains "plan says new run" "${out}" "start a new run"
expect "rise before the implementer" 1 "${h}" gate rise-u
contains "rise before: missing proposer" "${err}" "missing stage: proposer"
lacks "rise before: no new run" "${err}" "start a new run"
expect "plan before the implementer" 0 "${h}" plan rise-u
lacks "plan before: no new run" "${out}" "start a new run"
rise_sha="$(sed -n 's/^base_sha=//p' "${repo}/.harness/runs/rise-t/run")"
expect "start a replacing run" 0 "${h}" start rep-a --tool cursor --replaces rise-t
contains "replacing run keeps the base" "${out}" "base main pinned at ${rise_sha}"
contains "replacing run sees the work so far" "${out}" "profile=D1"
contains "replacing run recorded" "$(cat "${repo}/.harness/runs/rep-a/run")" "replaces=rise-t"
contains "replaced base recorded" "$(cat "${repo}/.harness/runs/rep-a/run")" "replaces_base=${rise_sha}"
mv "${repo}/.harness/runs/rise-t" "${work}/rise-t"
expect "replaced run deleted" 1 "${h}" gate rep-a
lacks "a deleted replaced run is no failure" "${err}" "differs from the base"
mv "${work}/rise-t" "${repo}/.harness/runs/rise-t"
rm "${repo}/src/c.go" "${repo}/src/d.go"
expect "start forced D1 run" 0 "${h}" start full-r --tool cursor --base main --full src/a.go
contains "forced D1" "${out}" "profile=D1"
expect "a replacing run keeps --full" 0 "${h}" start rep-f --tool cursor --replaces full-r
contains "replacing run stays D1" "${out}" "profile=D1"
contains "full copied" "$(cat "${repo}/.harness/runs/rep-f/run")" "full=1"
expect "a replacing run keeps the tool" 1 "${h}" start rep-g --tool claude --replaces full-r
contains "tool message" "${err}" "a replacing run keeps its tool cursor"
expect "a replacing run may name the same base" 0 "${h}" start rep-h --tool cursor --replaces full-r --base main
contains "same base accepted" "$(cat "${repo}/.harness/runs/rep-h/run")" "replaces=full-r"
expect "no run to replace" 1 "${h}" start rep-e --tool cursor --replaces nope
contains "missing run message" "${err}" "no run nope to replace"

expect "start D1 order run" 0 "${h}" start order-g --tool cursor --base main src/a.go src/b.go
"${h}" record order-g challenger gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record order-g proposer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record order-g implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record order-g challenger gemini-4-argon-medium google 0,0,0 --model gemini-4-argon >/dev/null
"${h}" record order-g critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "planning out of order" 1 "${h}" gate order-g
contains "challenger before proposer" "${err}" "order: challenger launched before proposer"
contains "challenger after implementer" "${err}" "order: challenger launched after the implementer"

# a tampered run file or log is refused
expect "start tamper run" 0 "${h}" start tamper-h --tool cursor --base main src/a.go
"${h}" record tamper-h implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
printf '7\tcritic\tgpt-5.6-sol-medium\topenai\t0,0,0\t\n' >>"${repo}/.harness/runs/tamper-h/log.tsv"
expect "log out of sequence" 1 "${h}" gate tamper-h
contains "log message" "${err}" "log.tsv:2: not a record or reject line"
cp "${repo}/.harness/runs/tamper-h/run" "${work}/run"
sed 's/^class=.*/class=D a[$(touch pwned)]/' "${work}/run" >"${repo}/.harness/runs/tamper-h/run"
expect "corrupt class" 1 "${h}" gate tamper-h
contains "corrupt message" "${err}" "corrupt run file"
sed 's/^base_sha=.*/base_sha=/' "${work}/run" >"${repo}/.harness/runs/tamper-h/run"
expect "base-less run in git" 1 "${h}" gate tamper-h
contains "base-less message" "${err}" "run without a base commit"

# the snapshot of a run with a base commit must equal the base commit's tables
expect "start snapshot run" 0 "${h}" start snap-p --tool cursor --base main lib/auth/x.go
snap="${repo}/.harness/runs/snap-p/tables"
cp "${snap}/profiles.tsv" "${work}/snap-profiles"
awk -F '\t' -v OFS='\t' '$1 == "D2" { $2 = "implementer critic"; $3 = 0 } 1' "${work}/snap-profiles" >"${snap}/profiles.tsv"
expect "tampered snapshot" 1 "${h}" gate snap-p
contains "tamper message" "${err}" "snapshot profiles.tsv differs from .agents/harness/profiles.tsv at the base commit"
expect "tampered snapshot refuses record" 1 "${h}" record snap-p implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5
cp "${work}/snap-profiles" "${snap}/profiles.tsv"
expect "restored snapshot" 0 "${h}" plan snap-p
rm -rf "${snap}"
expect "missing snapshot" 1 "${h}" gate snap-p
contains "missing snapshot message" "${err}" "the table snapshot is missing"
ln -s "${repo}/.agents/harness" "${snap}"
expect "linked snapshot" 1 "${h}" plan snap-p

# logs written before --model are read, their launches flagged; a line without a model after one with is refused
expect "start legacy run" 0 "${h}" start old-s --tool cursor --base main src/a.go
printf '1\timplementer\tclaude-opus-5-5-high\tanthropic\tdone\t\n' >"${repo}/.harness/runs/old-s/log.tsv"
"${h}" record old-s critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "legacy log read" 0 "${h}" gate old-s
contains "unchecked note" "${out}" "note: 1 launch(es) recorded before --model"
printf '3\tcritic\tgpt-5.6-sol-medium\topenai\t0,0,0\t\n' >>"${repo}/.harness/runs/old-s/log.tsv"
expect "legacy line after a checked one" 1 "${h}" gate old-s
contains "legacy line message" "${err}" "log.tsv:3: not a record or reject line"
expect "start dash model run" 0 "${h}" start dash-s --tool cursor --base main src/a.go
"${h}" record dash-s implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
printf '2\tcritic\tgpt-5.6-sol-medium\topenai\t0,0,0\t\t-\n' >>"${repo}/.harness/runs/dash-s/log.tsv"
expect "a dash as reported model in the log" 1 "${h}" gate dash-s
contains "dash line message" "${err}" "log.tsv:2: not a record or reject line"

# D2 run on opencode: two challenger families, never the Proposer's
expect "start D2" 0 "${h}" start sec-d --tool opencode --base main src/auth/x.go
"${h}" record sec-d proposer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sec-d challenger github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
"${h}" record sec-d challenger github-copilot/claude-opus-5.5 anthropic 0,0,0 --model claude-opus-5.5 --debt "copilot quota" >/dev/null
"${h}" record sec-d implementer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sec-d critic github-copilot/gemini-4-argon google 0,0,0 --model gemini-4-argon >/dev/null
"${h}" record sec-d security-critic github-copilot/gemini-4-argon google 0,0,0 --model gemini-4-argon >/dev/null
expect "D2 gate" 1 "${h}" gate sec-d
contains "proposer family" "${err}" "challenger of the Proposer's family anthropic"
contains "security family" "${err}" "security critic of family google"
contains "debt written" "$(cat "${repo}/.agents/harness/debt.tsv")" $'sec-d\topencode\tchallenger'

expect "start D2 one challenger" 0 "${h}" start sec-e --tool opencode --base main src/auth/x.go
"${h}" record sec-e proposer github-copilot/claude-opus-5.5 anthropic done --model claude-opus-5.5 >/dev/null
"${h}" record sec-e challenger github-copilot/gpt-5.6-sol openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "one challenger is not two" 1 "${h}" gate sec-e
contains "two needed" "${err}" "challengers: 2 of distinct family needed, got [openai]"

# the base is pinned: moving the branch after the fact does not empty the diff
git -C "${repo}" branch base0 main
expect "start pinned run" 0 "${h}" start pin-i --tool claude --base base0 src/a.go
contains "pinned lite" "${out}" "profile=lite"
mkdir -p "${repo}/src/auth"
printf 'x\n' >"${repo}/src/auth/k.go"
git -C "${repo}" add src/auth/k.go
git -C "${repo}" -c user.email=t@t -c user.name=t commit -q -m auth
git -C "${repo}" branch -f base0 HEAD
expect "plan after moving the base" 0 "${h}" plan pin-i
contains "pinned diff still D2" "${out}" "profile=D2"
pin_sha="$(sed -n 's/^base_sha=//p' "${repo}/.harness/runs/pin-i/run")"
expect "a replacing run refuses another base" 1 "${h}" start rep-c --tool claude --replaces pin-i --base base0
contains "other base message" "${err}" "is not the base of run pin-i"
expect "a replacing run keeps the pinned commit" 0 "${h}" start rep-d --tool claude --replaces pin-i
contains "old commit kept" "${out}" "base base0 pinned at ${pin_sha}"
cp "${repo}/.harness/runs/rep-d/run" "${work}/rep-d-run"
sed "s/^replaces_base=.*/replaces_base=$(git -C "${repo}" rev-parse base0)/" "${work}/rep-d-run" >"${repo}/.harness/runs/rep-d/run"
expect "a base other than the replaced one" 1 "${h}" gate rep-d
contains "replaced base message" "${err}" "differs from the base kept from the replaced run pin-i"
cp "${work}/rep-d-run" "${repo}/.harness/runs/rep-d/run"
cp "${repo}/.harness/runs/pin-i/run" "${work}/pin-i-run"
sed "s/^base_sha=.*/base_sha=$(git -C "${repo}" rev-parse base0)/" "${work}/pin-i-run" >"${repo}/.harness/runs/pin-i/run"
expect "a replaced run with another base" 1 "${h}" gate rep-d
contains "replaced run base message" "${err}" "base ${pin_sha} differs from the base of the replaced run pin-i"
cp "${work}/pin-i-run" "${repo}/.harness/runs/pin-i/run"

# the rules judging a run come from the base commit, and the stricter class table wins
cp "${repo}/.agents/harness/profiles.tsv" "${work}/profiles.tsv"
cp "${repo}/.agents/harness/classes.tsv" "${work}/classes.tsv"
printf 'D0\t*\n' | cat - "${work}/classes.tsv" >"${repo}/.agents/harness/classes.tsv"
expect "loosened class table" 0 "${h}" classify src/auth/x.go
contains "current table loosened" "${out}" D0
expect "start under base rules" 0 "${h}" start snap-j --tool cursor --base main lib/auth/x.go
contains "base classes win" "${out}" "class=D2"
awk -F '\t' -v OFS='\t' '$1 == "D2" { $2 = "implementer critic"; $3 = 0 } 1' "${work}/profiles.tsv" >"${repo}/.agents/harness/profiles.tsv"
"${h}" record snap-j implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record snap-j critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol >/dev/null
expect "loosened profile ignored" 1 "${h}" gate snap-j
contains "snapshot profile" "${err}" "missing stage: proposer"
cp "${work}/profiles.tsv" "${repo}/.agents/harness/profiles.tsv"
cp "${work}/classes.tsv" "${repo}/.agents/harness/classes.tsv"

# debt.tsv leaves the diff only while rows are appended to it
git -C "${repo}" add .agents/harness/debt.tsv
git -C "${repo}" -c user.email=t@t -c user.name=t commit -q -m debt
cp "${repo}/.agents/harness/debt.tsv" "${work}/debt-base"
expect "start debt ledger run" 0 "${h}" start ledger-q --tool cursor --base main src/a.go
"${h}" record ledger-q implementer claude-opus-5-5-high anthropic done --model claude-opus-5-5 >/dev/null
"${h}" record ledger-q critic gpt-5.6-sol-medium openai 0,0,0 --model gpt-5.6-sol --debt "test" >/dev/null
expect "appended debt keeps lite" 0 "${h}" gate ledger-q
contains "appended lite" "${out}" "profile lite"
cp "${repo}/.agents/harness/debt.tsv" "${work}/debt-appended"
grep -v $'\tsec-d\t' "${work}/debt-appended" >"${repo}/.agents/harness/debt.tsv"
expect "deleted debt row raises the class" 1 "${h}" gate ledger-q
contains "deleted row D2" "${err}" "profile D2"
cp "${work}/debt-appended" "${repo}/.agents/harness/debt.tsv"

# outside git: named paths, no base
plain="${work}/plain"
mkdir -p "${plain}/scripts"
cp -R "${src}/.agents" "${plain}/.agents"
grep '^#' "${src}/.agents/harness/debt.tsv" >"${plain}/.agents/harness/debt.tsv" || true
cp "${src}/scripts/harness.sh" "${plain}/scripts/harness.sh"
expect "start outside git" 0 "${plain}/scripts/harness.sh" start p-k --tool claude src/a.go
contains "outside git lite" "${out}" "profile=lite"
expect "replace outside git" 0 "${plain}/scripts/harness.sh" start p-m --tool claude --replaces p-k
contains "replaced outside git" "$(cat "${plain}/.harness/runs/p-m/run")" "replaces=p-k"
contains "paths carried over" "$(cat "${plain}/.harness/runs/p-m/paths")" "src/a.go"
expect "start a run to lose its paths" 0 "${plain}/scripts/harness.sh" start p-o --tool claude src/a.go src/b.go
rm "${plain}/.harness/runs/p-o/paths"
expect "replace a run without paths" 1 "${plain}/scripts/harness.sh" start p-q --tool claude --replaces p-o src/a.go
contains "no paths message" "${err}" "run p-o has no paths file"
expect "base outside git" 1 "${plain}/scripts/harness.sh" start p-l --tool claude --base main src/a.go
cut -f 1-4 "${plain}/.harness/runs/p-k/tables/models.tsv" >"${work}/old-models"
cp "${work}/old-models" "${plain}/.harness/runs/p-k/tables/models.tsv"
expect "snapshot without expect" 1 "${plain}/scripts/harness.sh" record p-k implementer opus anthropic done --model claude-opus-5-5
contains "no expect message" "${err}" "has no expect column for opus"
expect "snapshot without expect, with debt" 0 "${plain}/scripts/harness.sh" record p-k implementer opus anthropic done --model claude-opus-5-5 --debt "old run"

# sync and check
cp "${repo}/.agents/harness/roles.tsv" "${work}/roles.tsv"
expect "check before sync" 1 "${h}" check
expect "sync" 0 "${h}" sync
expect "check after sync" 0 "${h}" check
contains "claude critic model" "$(cat "${repo}/.claude/agents/harness-critic-sonnet.md")" "model: sonnet"
contains "claude critic read-only" "$(cat "${repo}/.claude/agents/harness-critic-sonnet.md")" $'tools: Read, Grep, Glob\n'
lacks "no fable agent generated" "$(ls "${repo}/.claude/agents")" "fable"
lacks "claude reviewer has no shell" "$(cat "${repo}/.claude/agents/harness-proposer-opus.md")" "Bash"
lacks "claude implementer can write" "$(cat "${repo}/.claude/agents/harness-implementer-opus.md")" "tools:"
contains "cursor readonly" "$(cat "${repo}/.cursor/agents/harness-critic-gpt-5-6-sol-medium.md")" "readonly: true"
oc="$(cat "${repo}/.opencode/agent/harness-critic-gpt-5-6-sol.md")"
contains "opencode deny edit" "${oc}" "edit: deny"
contains "opencode deny bash" "${oc}" "bash: deny"
contains "opencode deny task" "${oc}" "task: deny"
lacks "opencode no git allowlist" "${oc}" "git diff"
contains "skill link" "$(readlink "${repo}/.claude/skills/multi-agent")" "../../.agents/skills/multi-agent"
# frontmatter: every value is a plain scalar without `: ` or ` #`, or a double-quoted one
bad_yaml="$(cd "${repo}" && for f in .claude/agents/harness-*.md .cursor/agents/harness-*.md .opencode/agent/harness-*.md; do
  awk -v f="${f}" 'NR == 1 && $0 != "---" { print f ": no frontmatter"; exit }
    NR > 1 && $0 == "---" { exit }
    NR > 1 && /^[a-z]+: / { v = $0; sub(/^[a-z]+: /, "", v)
      if (v ~ /^"/) { if (v !~ /^"([^"\\]|\\.)*"$/) print f ": bad quoted " $0 }
      else if (v ~ /: / || v ~ / #/) print f ": unquoted " $0 }' "${f}"
done)"
if [[ -z "${bad_yaml}" ]]; then ok; else bad "frontmatter: ${bad_yaml}"; fi
contains "proposer description quoted" "$(cat "${repo}/.claude/agents/harness-proposer-opus.md")" 'description: "Plans a change: resolves'

# sync never writes through a symbolic link
printf 'keep\n' >"${work}/outside"
ln -sf "${work}/outside" "${repo}/.claude/agents/harness-explorer-haiku.md"
expect "sync replaces a linked agent file" 0 "${h}" sync
contains "link target untouched" "$(cat "${work}/outside")" "keep"
if [[ ! -L "${repo}/.claude/agents/harness-explorer-haiku.md" ]]; then ok; else bad "agent file still a link"; fi
mv "${repo}/.cursor/agents" "${work}/cursor-agents"
ln -s "${work}/cursor-agents" "${repo}/.cursor/agents"
expect "sync refuses a linked agent directory" 1 "${h}" sync
contains "link message" "${err}" ".cursor/agents is a symbolic link"
rm "${repo}/.cursor/agents"
mv "${work}/cursor-agents" "${repo}/.cursor/agents"
rm "${repo}/.claude/skills/multi-agent"
mkdir -p "${repo}/.claude/skills/multi-agent"
printf 'mine\n' >"${repo}/.claude/skills/multi-agent/SKILL.md"
expect "sync refuses to replace a real skill directory" 1 "${h}" sync
contains "skill kept" "$(cat "${repo}/.claude/skills/multi-agent/SKILL.md")" "mine"
rm -rf "${repo}/.claude/skills/multi-agent"
expect "sync relinks" 0 "${h}" sync
contains "critic states its model" "$(cat "${repo}/.claude/agents/harness-critic-sonnet.md")" 'Second line, exactly: `Model: <id>`'
contains "implementer states its model" "$(cat "${repo}/.claude/agents/harness-implementer-opus.md")" 'First line of your answer, exactly: `Model: <id>`'
cp "${repo}/.claude/agents/harness-explorer-haiku.md" "${work}/haiku.md"
ln -sf "${work}/haiku.md" "${repo}/.claude/agents/harness-explorer-haiku.md"
expect "check sees a linked agent file" 1 "${h}" check
contains "linked file is drift" "${err}" "stale .claude/agents/harness-explorer-haiku.md"
expect "sync replaces the link" 0 "${h}" sync
expect "check after replacing the link" 0 "${h}" check
printf 'cursor\tcritic\tgpt-6-sol-medium\topenai\t*gpt-6-sol*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check sees a new model" 1 "${h}" check
contains "drift" "${err}" "harness-critic-gpt-6-sol-medium"
printf 'claude\tcritic\tgpt-6-sol\topenai\t*gpt-6*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses a non-Anthropic claude subagent" 1 "${h}" check
contains "anthropic only" "${err}" "Claude Code subagents run Anthropic models by alias only"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'cursor\tcritic\tgpt-7\topenai\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses a row without expect" 1 "${h}" check
contains "expect column message" "${err}" "not tool<TAB>role<TAB>slug<TAB>family<TAB>expect"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'cursor\tcritic\tgpt-7\topenai\t*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses a match-all expect" 1 "${h}" check
contains "glob message" "${err}" "bad expect glob *"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'claude\tcritic\tclaude-sonnet-5-5\tanthropic\tclaude-sonnet-*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses a claude model id as slug" 1 "${h}" check
contains "alias message" "${err}" "by alias only"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'cursor\tcritic\tgpt-5-6-sol-medium\topenai\t*gpt-5?6-sol*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses two slugs with one agent file" 1 "${h}" check
contains "handle collision message" "${err}" "give the same agent file"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'cursor\tcritic\tGPT-5.6-sol-medium\topenai\t*gpt-5?6-sol*\n' >>"${repo}/.agents/harness/models.tsv"
expect "check refuses slugs differing by case" 1 "${h}" check
contains "case collision message" "${err}" "give the same agent file"
git -C "${repo}" checkout -q -- .agents/harness/models.tsv
printf 'D2\t.agents/*\textra\n' >>"${repo}/.agents/harness/classes.tsv"
expect "check refuses a classes row with a third field" 1 "${h}" check
contains "classes row message" "${err}" "not D0|D1|D2<TAB>glob"
git -C "${repo}" checkout -q -- .agents/harness/classes.tsv
awk -F '\t' -v OFS='\t' '$1 == "critic" { $2 = "RO" } 1' "${work}/roles.tsv" >"${repo}/.agents/harness/roles.tsv"
expect "check refuses a bad access" 1 "${h}" check
contains "access message" "${err}" "roles.tsv:"
cp "${work}/roles.tsv" "${repo}/.agents/harness/roles.tsv"
awk -F '\t' -v OFS='\t' '$1 == "D1" { $3 = "one" } 1' "${work}/profiles.tsv" >"${repo}/.agents/harness/profiles.tsv"
expect "check refuses a bad count" 1 "${h}" check
contains "profiles message" "${err}" "profiles.tsv:"
awk -F '\t' -v OFS='\t' '$1 == "D2" { $3 = 3 } 1' "${work}/profiles.tsv" >"${repo}/.agents/harness/profiles.tsv"
expect "check refuses too few challenger models" 1 "${h}" check
contains "keys message" "${err}" "claude/challenger: no model left"
cp "${work}/profiles.tsv" "${repo}/.agents/harness/profiles.tsv"
expect "check after restoring the tables" 0 "${h}" check

printf '%s passed, %s failed\n' "${passes}" "${fails}"
((fails == 0))
