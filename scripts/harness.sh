#!/usr/bin/env bash
# Deterministic multi-agent harness for Claude Code, Cursor and OpenCode (skill multi-agent). Tables in .agents/harness/.
#   scripts/harness.sh classify [--base <ref>] [<path>...]                  # D0|D1|D2 of the paths, or of the diff
#   scripts/harness.sh pick <tool> <role> [--count <n>] [--exclude <key>]... [--skip <slug>]...
#   scripts/harness.sh start <run> --tool <tool> [--base <ref> | --replaces <run>] [--full] [<path>...]  # base in git
#   scripts/harness.sh plan <run>                                            # stages, models, budget, launches so far
#   scripts/harness.sh record <run> <role> <slug> <family> <done|B,S,N> --model <reported id> [--debt <reason>]
#   scripts/harness.sh reject <run> <role> <slug> "<file:line> <why>"        # one open BLOCKER of that slug's rounds
#   scripts/harness.sh gate <run>                                            # every rule of the run's profile holds
#   scripts/harness.sh sync | check                                          # generate agent files | verify tables and files
# Every role runs as a native subagent of the tool driving the run: pick prints `<handle><TAB><slug><TAB><family>`
# lines, <handle> being the generated subagent to launch. Independent roles (Challengers against the Proposer, critics
# against the Implementer) differ by the tool's independence key of tools.tsv: the family, or the model where a tool
# has a single family (Claude Code). Runs live in .harness/runs/<run>/ (local, gitignored): the run pins the base
# commit and a snapshot of the tables (from that commit in git) so a change cannot loosen the rules that judge it.
# The run log is attested by the orchestrator: the gate checks its consistency, not that the launches happened.
# Each launch is recorded with the model id the subagent reported on its `Model:` line, checked against the glob of
# its row: a tool that ignores the generated model (a stale agent definition, a fallback) is caught at record.
# Bash 3.2 compatible.
# Exit 0 ok, 1 refused or failed (reason on stderr), 2 usage. Tests: scripts/test/harness.sh.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tables="${root}/.agents/harness"
models="${tables}/models.tsv"
tools_tsv="${tables}/tools.tsv"
roles_tsv="${tables}/roles.tsv"
classes="${tables}/classes.tsv"
profiles="${tables}/profiles.tsv"
debt="${tables}/debt.tsv"
runs="${root}/.harness/runs"
readonly FAMILIES="anthropic openai google xai cursor"
readonly RUN_RE='^[a-z0-9][a-z0-9-]{0,47}$'
readonly WORD_RE='^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$'
readonly MODEL_RE='^[A-Za-z0-9._/:-]{1,128}$'

usage() {
  sed -n '3,10p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2
  exit 2
}
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

entries() { awk -F '\t' '!/^#/ && NF' "$1"; }
role_field() { awk -F '\t' -v r="$1" -v f="$2" '!/^#/ && $1 == r { print $f; exit }' "${roles_tsv}"; }
is_role() { [[ "$1" =~ ^[a-z-]+$ && -n "$(role_field "$1" 2)" ]]; }
is_tool() { [[ "$1" =~ ^[a-z]+$ && -n "$(tool_key "$1")" ]]; }
# tool_key tool: the column of models.tsv and of a run log holding the tool's independence key, 3 (slug) or 4 (family).
tool_key() {
  awk -F '\t' -v t="$1" '!/^#/ && $1 == t { if ($2 == "model") print 3; else if ($2 == "family") print 4; exit }' "${tools_tsv}"
}

# handle tool role slug: the generated subagent's name.
handle() {
  local short="${3#github-copilot/}"
  short="${short//[\/.]/-}"
  printf 'harness-%s-%s' "$2" "${short}"
}

# pick tool role count excluded skipped: picked, the first count entries of distinct keys outside the excluded keys
# and skipped slugs, as handle/slug/family lines, and taken, their keys; return 1 when fewer are left.
pick() {
  local tool="$1" role="$2" count="$3" excluded=" $4 " skipped=" $5 " col t r slug family expect key found=0
  col="$(tool_key "${tool}")"
  picked="" taken=" "
  while IFS=$'\t' read -r t r slug family expect; do
    if ((found >= count)); then break; fi
    [[ "${t}" == "${tool}" && "${r}" == "${role}" ]] || continue
    if [[ "${col}" == 3 ]]; then key="${slug}"; else key="${family}"; fi
    [[ "${excluded}" != *" ${key} "* && "${skipped}" != *" ${slug} "* && "${taken}" != *" ${key} "* ]] || continue
    picked+="$(handle "${t}" "${r}" "${slug}")"$'\t'"${slug}"$'\t'"${family}"$'\n'
    taken+="${key} "
    found=$((found + 1))
  done <<<"$(entries "${models}")"
  if ((found < count)); then
    printf 'error: %s/%s: no model left\n' "${tool}" "${role}" >&2
    return 1
  fi
}

# norm_path path: normed, the path relative to the root (`./`, `//`, `/./` and a `${root}/` prefix removed);
# dies on a path outside the root or with a `..` segment, which would dodge the globs of classes.tsv.
norm_path() {
  local p="$1"
  [[ "${p}" != *$'\n'* ]] || die "path with a newline"
  if [[ "${p}" == "${root}/"* ]]; then p="${p#"${root}"/}"; fi
  while [[ "${p}" == *//* ]]; do p="${p//\/\//\/}"; done
  while [[ "${p}" == */./* ]]; do p="${p//\/.\//\/}"; done
  while [[ "${p}" == ./* ]]; do p="${p#./}"; done
  [[ "${p}" != /* ]] || die "path outside the repository: $1"
  [[ "/${p}/" != */../* ]] || die "path with a .. segment: $1"
  [[ -n "${p}" && "${p}" != . ]] || die "empty path: $1"
  normed="${p}"
}

# class_of table...: the class of the paths on stdin, one per line (D0 for none): per path, the first matching line
# of each table, the highest across tables.
class_of() {
  local best=0 level lv path class glob t
  while IFS= read -r path; do
    [[ -n "${path}" ]] || continue
    level=0
    for t in "$@"; do
      lv=1
      while IFS=$'\t' read -r class glob; do
        # shellcheck disable=SC2053 # glob match on purpose
        if [[ "${path}" == ${glob} ]]; then
          lv="${class#D}"
          break
        fi
      done <<<"$(entries "${t}")"
      if ((lv > level)); then level="${lv}"; fi
    done
    if ((level > best)); then best="${level}"; fi
  done
  printf 'D%s\n' "${best}"
}
# run_class: class_of under the run's snapshot and the current classes.tsv, the stricter wins.
run_class() { class_of "${classes}" "${tables}/classes.tsv"; }

in_git() { git -C "${root}" rev-parse --git-dir >/dev/null 2>&1; }

# base_of ref: the merge base of ref and HEAD, as a full sha.
base_of() {
  in_git || die "not a git repository: name the paths instead of --base"
  git -C "${root}" merge-base "$1" HEAD 2>/dev/null || die "no merge base between $1 and HEAD"
}

# debt_appended sha: debt.tsv only grew since commit sha (absent there, or its content there a prefix of the file).
debt_appended() {
  local size rel=".agents/harness/debt.tsv"
  size="$(git -C "${root}" cat-file -s "$1:${rel}" 2>/dev/null)" || return 0
  [[ -f "${debt}" && ! -L "${debt}" ]] || return 1
  head -c "${size}" "${debt}" | cmp -s - <(git -C "${root}" show "$1:${rel}")
}

# diff_paths sha: paths whose content differs from commit sha, committed or not, untracked included; the model
# debt ledger is bookkeeping of the harness, not part of the change, while rows are only appended to it.
diff_paths() {
  local p skip_debt=0
  if debt_appended "$1"; then skip_debt=1; fi
  {
    git -C "${root}" diff -z --name-only --no-renames "$1"
    git -C "${root}" ls-files -z --others --exclude-standard
  } | while IFS= read -r -d '' p; do
    [[ "${p}" != *$'\n'* ]] || die "path with a newline in the diff"
    [[ "${p}" == .agents/harness/debt.tsv && "${skip_debt}" == 1 ]] || printf '%s\n' "${p}"
  done | sort -u
}

# profile_of class paths_count full
profile_of() {
  if [[ "$1" == D2 ]]; then
    printf 'D2'
  elif [[ "$3" == 0 && ("$1" == D0 || "$2" -le 1) ]]; then
    printf 'lite'
  else
    printf 'D1'
  fi
}
profile_rank() {
  case "$1" in
    lite) printf 0 ;;
    D1) printf 1 ;;
    *) printf 2 ;;
  esac
}
profile_field() { awk -F '\t' -v p="$1" -v f="$2" '!/^#/ && $1 == p { print $f; exit }' "${profiles}"; }

snapshot_tables="tools models roles classes profiles"
# load_run run: tool key base base_sha class full paths_count dir log of the run; the table variables point at the
# run's snapshot. With a base commit the snapshot must equal that commit's tables wherever the commit has them; only
# a base-less run started before snapshots falls back to the current tables.
load_run() {
  local t rel
  [[ "$1" =~ ${RUN_RE} ]] || usage
  dir="${runs}/$1"
  [[ -f "${dir}/run" ]] || die "no run $1: scripts/harness.sh start $1 first"
  base="$(sed -n 's/^base=//p' "${dir}/run")"
  base_sha="$(sed -n 's/^base_sha=//p' "${dir}/run")"
  class="$(sed -n 's/^class=//p' "${dir}/run")"
  full="$(sed -n 's/^full=//p' "${dir}/run")"
  [[ "${class}" =~ ^D[012]$ && "${full}" =~ ^[01]$ ]] || die "run $1: corrupt run file"
  [[ -z "${base_sha}" || "${base_sha}" =~ ^[0-9a-f]{40,64}$ ]] || die "run $1: corrupt base_sha"
  if [[ -n "${base_sha}" ]]; then
    in_git || die "the run has a base commit but ${root} is not a git repository"
    [[ -d "${dir}/tables" && ! -L "${dir}/tables" ]] || die "run $1: the table snapshot is missing: start a new run"
  fi
  if [[ -d "${dir}/tables" ]]; then
    for t in ${snapshot_tables}; do
      [[ -f "${dir}/tables/${t}.tsv" && ! -L "${dir}/tables/${t}.tsv" ]] || die "run $1: snapshot lacks ${t}.tsv"
      rel=".agents/harness/${t}.tsv"
      if [[ -n "${base_sha}" ]] && git -C "${root}" cat-file -e "${base_sha}:${rel}" 2>/dev/null &&
        ! git -C "${root}" show "${base_sha}:${rel}" | cmp -s - "${dir}/tables/${t}.tsv"; then
        die "run $1: snapshot ${t}.tsv differs from ${rel} at the base commit: the run's rules were edited, start a new run"
      fi
    done
    tools_tsv="${dir}/tables/tools.tsv" models="${dir}/tables/models.tsv" roles_tsv="${dir}/tables/roles.tsv"
    classes="${dir}/tables/classes.tsv" profiles="${dir}/tables/profiles.tsv"
  fi
  tool="$(sed -n 's/^tool=//p' "${dir}/run")"
  is_tool "${tool}" || die "run $1: tool ${tool} is not in tools.tsv"
  key="$(tool_key "${tool}")"
  paths_count="$(grep -c . "${dir}/paths" || true)"
  log="${dir}/log.tsv"
  touch "${log}"
}

# effective: class and profile now, raised by the diff against the pinned base commit; never lowered. planned_profile
# is the profile the run started with.
effective() {
  local now count
  eff_class="${class}"
  count="${paths_count}"
  if [[ -n "${base_sha}" ]]; then
    in_git || die "the run has a base commit but ${root} is not a git repository"
    now="$(diff_paths "${base_sha}" | run_class)"
    count="$(diff_paths "${base_sha}" | grep -c . || true)"
    if ((${now#D} > ${eff_class#D})); then eff_class="${now}"; fi
    if ((count < paths_count)); then count="${paths_count}"; fi
  fi
  eff_profile="$(profile_of "${eff_class}" "${count}" "${full}")"
  planned_profile="$(profile_of "${class}" "${paths_count}" "${full}")"
  if [[ "$(profile_rank "${eff_profile}")" -lt "$(profile_rank "${planned_profile}")" ]]; then
    eff_profile="${planned_profile}"
  fi
}

# A run log line: `n<TAB>role<TAB>slug<TAB>family<TAB>result<TAB>note<TAB>reported model`, a reject being
# `n<TAB>role<TAB>slug<TAB>-<TAB>reject<TAB>proof<TAB>-`, slug naming the rounds whose BLOCKER it closes (`-` in logs
# written before slugs: such a reject closes nothing). Logs written before --model have no 7th field on their first
# lines; they are read, their launches counted as unchecked.
# keys_of role [after]: the independence keys of the role's launches numbered above after (0: all). A launch's key
# comes from the model it reported, not the slug it was recorded under (a --debt launch may name any slug): the key
# of the first row of the run's tool whose expect glob matches the reported id. A reported id that matches no row,
# and a line without a reported model, keep the recorded key.
keys_of() {
  awk -F '\t' -v r="$1" -v p="${2:-0}" -v k="${key}" -v t="${tool}" -v table="${models}" '
    FILENAME == table {
      if ($0 !~ /^#/ && $1 == t) {
        rows++
        if ($5 == "") next
        re = tolower($5); gsub(/\./, "[.]", re); gsub(/\?/, ".", re); gsub(/\*/, ".*", re)
        nre++; RE[nre] = "^" re "$"; KEY[nre] = $k
      }
      next
    }
    !rows { printf "error: the run models.tsv lists no %s model\n", t > "/dev/stderr"; failed = 1; exit 1 }
    $2 == r && $5 != "reject" && $1 + 0 > p {
      v = $k
      if (NF >= 7 && $7 != "-" && $7 != "") {
        id = tolower($7)
        for (i = 1; i <= nre; i++) if (id ~ RE[i]) { v = KEY[i]; break }
      }
      if (!seen[v]++) printf "%s ", v
    }
    END { if (!failed && !rows) { printf "error: the run models.tsv lists no %s model\n", t > "/dev/stderr"; exit 1 } }
  ' "${models}" "${log}"
}
launches() { awk -F '\t' '$5 != "reject"' "${log}" | grep -c . || true; }
# last_launch role: the number of the role's last launch, 0 for none.
last_launch() { awk -F '\t' -v r="$1" '$2 == r && $5 != "reject" { n = $1 + 0 } END { print n + 0 }' "${log}"; }

lock_held=""
# lock_log: record and reject of a run take turns, else two launches recorded at once take the same number. mkdir is
# atomic everywhere (flock is missing on macOS). A lock that looks stale (a pid file older than a minute, or no pid
# file for about a second) is moved into a new directory made with mkdir and judged there again, so a lock retaken in
# between is never deleted.
lock_log() {
  local lock="${dir}/lock" tries=0 nopid=0 pid
  while ! mkdir "${lock}" 2>/dev/null; do
    tries=$((tries + 1))
    if ((tries > 100)); then
      [[ -d "${lock}" ]] || die "cannot create the lock ${lock}"
      die "the log of this run is locked by process ${pid:-?}: wait a minute for a lock left by an ended command to go stale, then run the command again; delete the directory ${lock} only when no record or reject of the run is running"
    fi
    [[ -d "${lock}" ]] || continue
    pid="$(cat "${lock}/pid" 2>/dev/null || true)"
    if [[ -z "${pid}" ]]; then nopid=$((nopid + 1)); else nopid=0; fi
    if ((nopid >= 10)) || lock_old "${lock}/pid"; then
      nopid=0
      lock_take_stale "${lock}"
      continue
    fi
    sleep 0.1 2>/dev/null || sleep 1
  done
  (set -C && printf '%s\n' "$$" >"${lock}/pid") 2>/dev/null ||
    die "lost the lock ${lock} to another process: run the command again"
  lock_held="${lock}"
  trap 'if [[ -n "${lock_held}" && "$(cat "${lock_held}/pid" 2>/dev/null || true)" == "$$" ]]; then rm -r "${lock_held}" 2>/dev/null || true; fi' EXIT
}
# lock_take_stale lock: moves the lock into a new aside directory, deletes it there if it is stale (no pid file, or a
# pid file older than a minute: moving a directory may refresh its own time), else puts it back; dies rather than
# delete a live lock it cannot put back.
lock_take_stale() {
  local lock="$1" aside
  aside="${lock}.stale.$$.${RANDOM}"
  mkdir -m 700 "${aside}" 2>/dev/null || die "cannot create ${aside}: delete it if it exists, then run the command again"
  if ! mv "${lock}" "${aside}/lock" 2>/dev/null; then
    rmdir "${aside}" 2>/dev/null || true
    return 0
  fi
  if [[ ! -f "${aside}/lock/pid" ]] || lock_old "${aside}/lock/pid"; then
    rm -r "${aside}" 2>/dev/null || true
    return 0
  fi
  if [[ ! -e "${lock}" && ! -L "${lock}" ]] && mv "${aside}/lock" "${lock}" 2>/dev/null && [[ ! -e "${lock}/lock" ]]; then
    rmdir "${aside}" 2>/dev/null || true
    return 0
  fi
  die "the lock of this run was retaken while a live one was set aside: wait for running records of the run, then delete ${lock} and ${aside}"
}
# lock_check: the lock is still this process's, just before writing the log.
lock_check() {
  [[ "$(cat "${lock_held}/pid" 2>/dev/null || true)" == "$$" ]] ||
    die "lost the lock ${lock_held} to another process: run the command again"
}
# log_numbered n: dies unless the log is still numbered 1..N once line n is appended (a lost lock lets two commands
# take one number).
log_numbered() {
  local bad
  bad="$(awk -F '\t' '$1 != NR { print NR; exit }' "${log}")"
  [[ -z "${bad}" ]] ||
    die "#$1 written, but log.tsv line ${bad} is out of sequence (two commands took one number while the lock was lost): stop and tell the human, who renumbers ${log}; the gate fails until then"
}
# lock_old path: the file or directory was last changed more than a minute ago.
lock_old() { [[ -n "$(find "$1" -maxdepth 0 -mmin +1 2>/dev/null || true)" ]]; }

cmd_classify() {
  local base_ref="" list=""
  while (($# > 0)); do
    case "$1" in
      --base)
        (($# >= 2)) && [[ "$2" =~ ${WORD_RE} ]] || usage
        base_ref="$2"
        shift 2
        ;;
      -*) usage ;;
      *)
        norm_path "$1"
        list+="${normed}"$'\n'
        shift
        ;;
    esac
  done
  if [[ -n "${base_ref}" ]]; then list+="$(diff_paths "$(base_of "${base_ref}")")"$'\n'; fi
  [[ -n "${list//$'\n'/}" ]] || die "no paths: name them, or pass --base <ref>"
  printf '%s' "${list}" | class_of "${classes}"
}

cmd_pick() {
  local tool role count=1 excluded="" skipped="" col known
  (($# >= 2)) || usage
  tool="$1" role="$2"
  shift 2
  is_tool "${tool}" && is_role "${role}" || usage
  col="$(tool_key "${tool}")"
  while (($# > 0)); do
    (($# >= 2)) || usage
    case "$1" in
      --count) [[ "$2" =~ ^[1-3]$ ]] || usage; count="$2" ;;
      --exclude)
        known="$(awk -F '\t' -v t="${tool}" -v c="${col}" -v v="$2" '!/^#/ && $1 == t && $c == v { print "yes"; exit }' "${models}")"
        [[ -n "${known}" ]] || usage
        excluded+="$2 "
        ;;
      --skip) [[ "$2" =~ ${WORD_RE} ]] || usage; skipped+="$2 " ;;
      *) usage ;;
    esac
    shift 2
  done
  pick "${tool}" "${role}" "${count}" "${excluded}" "${skipped}" || exit 1
  printf '%s' "${picked}"
}

cmd_start() {
  local run tool_arg="" base_ref="" base_sha="" full_flag=0 list="" cls count prof t src new replaces="" rf old_sha=""
  local old_tool r open
  (($# >= 1)) || usage
  run="$1"
  shift
  [[ "${run}" =~ ${RUN_RE} ]] || usage
  while (($# > 0)); do
    case "$1" in
      --tool) (($# >= 2)) && is_tool "$2" || usage; tool_arg="$2"; shift 2 ;;
      --base) (($# >= 2)) && [[ "$2" =~ ${WORD_RE} ]] || usage; base_ref="$2"; shift 2 ;;
      --replaces) (($# >= 2)) && [[ "$2" =~ ${RUN_RE} ]] || usage; replaces="$2"; shift 2 ;;
      --full) full_flag=1; shift ;;
      -*) usage ;;
      *) norm_path "$1"; list+="${normed}"$'\n'; shift ;;
    esac
  done
  [[ -n "${tool_arg}" ]] || usage
  [[ ! -e "${runs}/${run}" ]] || die "run ${run} exists: pick another id, or continue it with plan"
  [[ -z "$(awk -F '\t' -v r="${run}" '!/^#/ && $2 == r' "${debt}")" ]] ||
    die "run id ${run} has model debt in debt.tsv: pick another id"
  if [[ -n "${replaces}" ]]; then
    rf="${runs}/${replaces}/run"
    [[ -f "${rf}" && ! -L "${rf}" ]] || die "no run ${replaces} to replace"
    old_tool="$(sed -n 's/^tool=//p' "${rf}")"
    [[ "${old_tool}" == "${tool_arg}" ]] ||
      die "run ${replaces} ran on ${old_tool}: a replacing run keeps its tool ${old_tool}, start it with --tool ${old_tool}"
    log="${runs}/${replaces}/log.tsv"
    if [[ -f "${log}" ]]; then
      for r in $(entries "${roles_tsv}" | awk -F '\t' '$3 == "findings" { print $1 }'); do
        open="$(open_blockers "${r}")"
        ((open == 0)) || die "run ${replaces} has ${open} open ${r} BLOCKER(s): reject or fix them in run ${replaces} first"
      done
    fi
    if [[ "$(sed -n 's/^full=//p' "${rf}")" == 1 ]]; then full_flag=1; fi
    old_sha="$(sed -n 's/^base_sha=//p' "${rf}")"
    if in_git; then
      [[ "${old_sha}" =~ ^[0-9a-f]{40,64}$ ]] ||
        die "run ${replaces} has no base commit to keep: start a new run with --base <branch>"
      if [[ -n "${base_ref}" ]]; then
        base_sha="$(base_of "${base_ref}")"
        [[ "${base_sha}" == "${old_sha}" ]] ||
          die "--base ${base_ref} is not the base of run ${replaces}: a replacing run keeps that base, drop --base"
      fi
      base_ref="$(sed -n 's/^base=//p' "${rf}")"
    fi
    [[ -f "${runs}/${replaces}/paths" ]] || die "run ${replaces} has no paths file: start a new run naming the paths"
    list+="$(cat "${runs}/${replaces}/paths")"$'\n'
  fi
  if in_git; then
    [[ -n "${base_ref}" ]] || die "in a git repository a run needs --base <branch>: the gate recomputes the class from the diff"
    if [[ -n "${replaces}" ]]; then base_sha="${old_sha}"; else base_sha="$(base_of "${base_ref}")"; fi
    list+="$(diff_paths "${base_sha}")"$'\n'
  elif [[ -n "${base_ref}" ]]; then
    die "not a git repository: name the paths instead of --base"
  fi
  list="$(printf '%s' "${list}" | awk 'NF && !seen[$0]++')"
  [[ -n "${list}" ]] || die "no paths: name the paths the change will touch (they set the class), or pass --base"
  new="${runs}/.${run}.new"
  rm -rf "${new}"
  mkdir -p "${new}/tables"
  for t in ${snapshot_tables}; do
    src="${new}/tables/${t}.tsv"
    if [[ -z "${base_sha}" ]] || ! git -C "${root}" show "${base_sha}:.agents/harness/${t}.tsv" >"${src}" 2>/dev/null; then
      cp "${tables}/${t}.tsv" "${src}"
    fi
  done
  classes="${new}/tables/classes.tsv"
  cls="$(printf '%s\n' "${list}" | run_class)"
  count="$(printf '%s\n' "${list}" | grep -c .)"
  prof="$(profile_of "${cls}" "${count}" "${full_flag}")"
  printf 'tool=%s\nbase=%s\nbase_sha=%s\nclass=%s\nfull=%s\nprofile=%s\nstarted=%s\n' "${tool_arg}" "${base_ref}" \
    "${base_sha}" "${cls}" "${full_flag}" "${prof}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${new}/run"
  if [[ -n "${replaces}" ]]; then printf 'replaces=%s\nreplaces_base=%s\n' "${replaces}" "${base_sha}" >>"${new}/run"; fi
  printf '%s\n' "${list}" >"${new}/paths"
  : >"${new}/log.tsv"
  mv "${new}" "${runs}/${run}"
  cmd_plan "${run}"
}

# default_key role excluded: the keys of the role's recorded launches, else of the model the table would pick for it.
default_key() {
  local recorded
  recorded="$(keys_of "$1")"
  if [[ -n "${recorded}" ]]; then
    printf '%s' "${recorded}"
  else
    pick "${tool}" "$1" 1 "$2" "" || exit 1
    printf '%s' "${taken// /}"
  fi
}

cmd_plan() {
  local run="$1" req need budget rounds role excl impl crit row
  load_run "${run}"
  effective
  req="$(profile_field "${eff_profile}" 2)"
  need="$(profile_field "${eff_profile}" 3)"
  budget="$(profile_field "${eff_profile}" 4)"
  rounds="$(profile_field "${eff_profile}" 5)"
  printf 'run %s  tool=%s  class=%s  profile=%s  budget=%s launches (%s used), %s critic rounds, independent by %s\n' \
    "${run}" "${tool}" "${eff_class}" "${eff_profile}" "${budget}" "$(launches)" "${rounds}" \
    "$([[ "${key}" == 3 ]] && printf model || printf family)"
  if [[ -n "${base_sha}" ]]; then printf 'base %s pinned at %s\n' "${base}" "${base_sha}"; fi
  if [[ "${eff_class}" != "${class}" ]]; then printf 'class rose from %s: the diff touches more than planned\n' "${class}"; fi
  if [[ "${eff_profile}" != "${planned_profile}" ]]; then printf 'profile rose from %s\n' "${planned_profile}"; fi
  late_stages "${req}" "${run}"
  printf 'role\thandle\tslug\tfamily\n'
  for role in ${req}; do
    excl=""
    case "${role}" in
      challenger) excl="$(default_key proposer "")" ;;
      critic) excl="$(default_key implementer "")" ;;
      security-critic)
        impl="$(default_key implementer "")"
        crit="$(keys_of critic)"
        [[ -n "${crit}" ]] || crit="$(default_key critic "${impl}")"
        excl="${impl} ${crit}"
        ;;
    esac
    if [[ "${role}" == challenger ]]; then
      pick "${tool}" challenger "${need}" "${excl}" "" || exit 1
    else
      pick "${tool}" "${role}" 1 "${excl}" "" || exit 1
    fi
    while IFS= read -r row; do
      if [[ -n "${row}" ]]; then printf '%s\t%s\n' "${role}" "${row}"; fi
    done <<<"${picked}"
  done
}

# expect_of tool role slug family: the expect glob of the row, empty for a model the table does not list.
expect_of() {
  awk -F '\t' -v t="$1" -v r="$2" -v s="$3" -v f="$4" \
    '!/^#/ && $1 == t && $2 == r && $3 == s && $4 == f { print ($5 == "" ? "-" : $5); exit }' "${models}"
}

# model_matches id glob: the reported id matches the glob, case ignored.
model_matches() {
  local rc=1
  shopt -s nocasematch
  # shellcheck disable=SC2053 # glob match on purpose
  if [[ "$1" == $2 ]]; then rc=0; fi
  shopt -u nocasematch
  return "${rc}"
}

cmd_record() {
  local run role slug family result note="" reason="" model="" output expect n own=""
  (($# >= 5)) || usage
  run="$1" role="$2" slug="$3" family="$4" result="$5"
  shift 5
  while (($# > 0)); do
    (($# >= 2)) || usage
    case "$1" in
      --model) [[ -z "${model}" ]] || usage; model="$2" ;;
      --debt) [[ -z "${reason}" && -n "$2" ]] || usage; reason="$2" ;;
      *) usage ;;
    esac
    shift 2
  done
  [[ -n "${model}" ]] || die "record needs --model <id>: the id on the subagent's Model: line, never the plan's slug"
  [[ "${model}" =~ ${MODEL_RE} && "${model}" != - ]] ||
    die "a reported model id is [A-Za-z0-9._/:-]+, at most 128 characters, never -: give --model the id the subagent reported"
  load_run "${run}"
  is_role "${role}" || usage
  [[ "${slug}" =~ ${WORD_RE} && " ${FAMILIES} " == *" ${family} "* ]] || usage
  [[ -z "${reason}" || "${reason}" =~ ^[[:print:]]+$ ]] || die "a debt reason is printable text on one line"
  output="$(role_field "${role}" 3)"
  if [[ "${output}" == findings ]]; then
    [[ "${result}" =~ ^[0-9]+,[0-9]+,[0-9]+$ ]] || die "${role} records its findings as B,S,N (BLOCKER, SHOULD-FIX, NIT counts)"
  else
    [[ "${result}" == done ]] || die "${role} records done"
  fi
  expect="$(expect_of "${tool}" "${role}" "${slug}" "${family}")"
  if [[ -n "${reason}" && "${output}" == findings ]]; then
    own="$(awk -F '\t' -v t="${tool}" -v r="${role}" -v s="${slug}" '!/^#/ && $1 == t && $2 == r && $3 == s && $5 != "" { print $5; exit }' "${models}")"
  fi
  lock_log
  if [[ -n "${own}" ]] && ! model_matches "${model}" "${own}" &&
    [[ -n "$(awk -F '\t' -v r="${role}" -v s="${slug}" '$2 == r && $3 == s && $5 != "reject" { print 1; exit }' "${log}")" ]]; then
    die "${model} does not match ${slug} (${own}), which already has a ${role} round in this run whose BLOCKERs this round would close: record the substitute under its own slug"
  fi
  if [[ -z "${reason}" ]]; then
    [[ -n "${expect}" ]] ||
      die "${slug} (${family}) is not a ${tool}/${role} model of models.tsv: a substitute needs the human's OK, then --debt <reason>"
    [[ "${expect}" != - ]] ||
      die "the run's models.tsv has no expect column for ${slug}: record --debt with the human's OK, or start a new run"
    model_matches "${model}" "${expect}" ||
      die "subagent reported ${model}, expected ${expect}: the tool did not run the planned model; restart the session after sync, or record --debt with the human's OK"
  else
    note="debt: ${reason}"
  fi
  lock_check
  if [[ -n "${reason}" ]]; then
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s; reported %s\n' "$(date -u +%Y-%m-%d)" "${run}" "${tool}" "${role}" "${slug}" \
      "${family}" "${reason}" "${model}" >>"${debt}"
  fi
  n=$(($(grep -c . "${log}" || true) + 1))
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${n}" "${role}" "${slug}" "${family}" "${result}" "${note}" "${model}" >>"${log}"
  log_numbered "${n}"
  printf 'recorded %s #%s: %s %s (%s) %s, reported %s\n' "${run}" "${n}" "${role}" "${slug}" "${family}" "${result}" "${model}"
}

# open_blockers role [slug]: the role's open BLOCKERs, of one slug if given, read in log order and kept per slug. A
# round sets its slug's count when a fix ran after that slug's previous round (a writing launch for a critic role, a
# Proposer launch for a Challenger), else adds to it; a reject takes one from the slug it names.
open_blockers() {
  local fixers
  if [[ "$1" == challenger ]]; then fixers="proposer"; else fixers="$(rw_roles)"; fi
  awk -F '\t' -v r="$1" -v want="${2-}" -v fixers=" ${fixers} " '
    { n = $1 + 0 }
    $5 != "reject" && index(fixers, " " $2 " ") { fixed = n }
    $2 != r { next }
    $5 == "reject" { if (o[$3] > 0) o[$3]--; next }
    { split($5, c, ","); if (fixed > s[$3]) o[$3] = c[1] + 0; else o[$3] += c[1] + 0; s[$3] = n }
    END { t = 0; for (k in o) if (want == "" || k == want) t += o[k]; print t }' "${log}"
}

# proof_line proof: dies unless the proof starts with file:line, a line of a file of the change's tree (tracked or
# untracked, never gitignored), followed by the reason.
proof_line() {
  local ref="${1%% *}" file line n
  [[ "$1" =~ ^[[:print:]]+$ && "$1" == *" "?* && "${ref}" =~ ^[^:]+:[1-9][0-9]{0,8}$ ]] ||
    die "a rejected BLOCKER needs in-code proof: \"<file:line> <why>\" (a test, a compiler or lint guarantee, a contract)"
  file="${ref%:*}" line="${ref##*:}"
  norm_path "${file}"
  file="${normed}"
  [[ -f "${root}/${file}" && ! -L "${root}/${file}" ]] || die "proof ${ref}: no file ${file}"
  [[ "${file}" != .harness/* ]] || die "proof ${ref}: the run log is no proof"
  if in_git && [[ -z "$(git -C "${root}" ls-files --cached --others --exclude-standard -- "${file}")" ]]; then
    die "proof ${ref}: ${file} is ignored by git"
  fi
  n="$(awk 'END { print NR }' "${root}/${file}")"
  ((line <= n)) || die "proof ${ref}: ${file} has ${n} lines"
}

cmd_reject() {
  local run role slug proof n
  (($# == 4)) || usage
  run="$1" role="$2" slug="$3" proof="$4"
  load_run "${run}"
  is_role "${role}" && [[ "$(role_field "${role}" 3)" == findings && "${slug}" =~ ${WORD_RE} ]] || usage
  proof_line "${proof}"
  lock_log
  [[ -n "$(awk -F '\t' -v r="${role}" -v s="${slug}" '$2 == r && $3 == s && $5 != "reject" { print 1; exit }' "${log}")" ]] ||
    die "no ${role} round recorded as ${slug}: name the slug of the round that raised the BLOCKER"
  (($(open_blockers "${role}" "${slug}") > 0)) || die "no open BLOCKER to reject for ${role} ${slug}"
  lock_check
  n=$(($(grep -c . "${log}" || true) + 1))
  printf '%s\t%s\t%s\t-\treject\t%s\t-\n' "${n}" "${role}" "${slug}" "${proof}" >>"${log}"
  log_numbered "${n}"
  printf 'recorded %s #%s: one %s BLOCKER of %s rejected with proof, %s left open\n' "${run}" "${n}" "${role}" \
    "${slug}" "$(open_blockers "${role}")"
}

# late_stages req run: once the Implementer ran, each stage req puts before it that has no launch; a class that rose
# after the Implementer ran cannot be satisfied in this run.
late_stages() {
  awk -F '\t' -v req="$1" -v run="$2" '
    $5 == "reject" { next }
    !($2 in first) { first[$2] = $1 + 0 }
    END {
      if (!("implementer" in first)) exit
      m = split(req, R, " ")
      for (i = 1; i <= m && R[i] != "implementer"; i++)
        if (!(R[i] in first))
          printf "order: %s is required before the implementer, which already ran (#%d): start a new run with --replaces %s, which keeps the tool, base, paths and --full of this run and launches the implementer again on the work so far\n", R[i], first["implementer"], run
    }' "${log}"
}

rw_roles() { awk -F '\t' '!/^#/ && NF && $2 == "rw" { printf "%s ", $1 }' "${roles_tsv}"; }

# order_fails req: the launch-order violations of the log for the required roles req. Roles before the Implementer
# in req run in that order and before the first Implementer launch; each review role after it has its last round
# after the last launch of any writing role.
order_fails() {
  awk -F '\t' -v req="$1" -v rw=" $(rw_roles)" '
    $5 == "reject" { next }
    { n = $1 + 0; if (!($2 in first)) first[$2] = n; last[$2] = n; if (index(rw, " " $2 " ")) lastw = n }
    END {
      m = split(req, R, " "); prev = ""; after = 0
      for (i = 1; i <= m; i++) {
        r = R[i]
        if (r == "implementer") { after = 1; continue }
        if (!(r in first)) continue
        if (!after) {
          if (prev != "" && (prev in first) && first[r] < first[prev]) printf "order: %s launched before %s\n", r, prev
          if (("implementer" in first) && last[r] > first["implementer"]) printf "order: %s launched after the implementer\n", r
          prev = r
        } else if (last[r] < lastw) {
          printf "order: the last %s round (#%d) precedes write launch #%d: review the final code\n", r, last[r], lastw
        }
      }
    }' "${log}"
}

# log_fails: lines of the log not written by record or reject; a line without the reported model only before any
# line with one.
log_fails() {
  awk -F '\t' '
    NF == 7 { withmodel = 1 }
    $1 != NR || (NF != 7 && (NF != 6 || withmodel)) || ($5 != "reject" && $5 != "done" && $5 !~ /^[0-9]+,[0-9]+,[0-9]+$/) ||
      (NF == 7 && ($5 == "reject" ? $7 != "-" : ($7 !~ /^[A-Za-z0-9._\/:-]+$/ || $7 == "-"))) {
      printf "log.tsv:%d: not a record or reject line\n", NR }' "${log}"
}

cmd_gate() {
  local run="$1" fails="" req need budget rounds role count prop impl crit chal k open n_debt n_logged unchecked what
  local lastprop fresh msg rep
  load_run "${run}"
  if [[ -z "${base_sha}" ]] && in_git; then
    fails+="run without a base commit in a git repository: start a new run with --base <branch>"$'\n'
  fi
  rep="$(sed -n 's/^replaces=//p' "${dir}/run")"
  if [[ -n "${rep}" && "$(sed -n 's/^replaces_base=//p' "${dir}/run")" != "${base_sha}" ]]; then
    fails+="base ${base_sha:-none} differs from the base kept from the replaced run ${rep}: start a new run with --replaces ${rep} and no --base"$'\n'
  fi
  if [[ "${rep}" =~ ${RUN_RE} && -f "${runs}/${rep}/run" ]] &&
    [[ "$(sed -n 's/^base_sha=//p' "${runs}/${rep}/run")" != "${base_sha}" ]]; then
    fails+="base ${base_sha:-none} differs from the base of the replaced run ${rep}: start a new run with --replaces ${rep} and no --base"$'\n'
  fi
  effective
  req="$(profile_field "${eff_profile}" 2)"
  need="$(profile_field "${eff_profile}" 3)"
  budget="$(profile_field "${eff_profile}" 4)"
  rounds="$(profile_field "${eff_profile}" 5)"
  what="$([[ "${key}" == 3 ]] && printf model || printf family)"
  fails+="$(log_fails)"$'\n'
  for role in ${req}; do
    count="$(awk -F '\t' -v r="${role}" '$2 == r && $5 != "reject"' "${log}" | grep -c . || true)"
    ((count > 0)) || fails+="missing stage: ${role} (profile ${eff_profile})"$'\n'
  done
  fails+="$(order_fails "${req}")"$'\n'
  fails+="$(late_stages "${req}" "${run}")"$'\n'
  chal="$(keys_of challenger)"
  prop="$(keys_of proposer)"
  if ((need > 0)); then
    lastprop="$(last_launch proposer)"
    fresh="$(keys_of challenger "${lastprop}")"
    set -- ${fresh}
    if (($# < need)); then
      msg="challengers: ${need} of distinct ${what} needed, got [${fresh% }]"
      if ((lastprop > 0)); then msg+=" after the last proposer launch (#${lastprop})"; fi
      fails+="${msg}"$'\n'
    fi
  fi
  for k in ${chal}; do
    if [[ " ${prop} " == *" ${k} "* ]]; then fails+="challenger of the Proposer's ${what} ${k}"$'\n'; fi
  done
  impl="$(keys_of implementer)"
  crit="$(keys_of critic)"
  for k in ${crit}; do
    if [[ " ${impl} " == *" ${k} "* ]]; then fails+="critic of the Implementer's ${what} ${k}"$'\n'; fi
  done
  for k in $(keys_of security-critic); do
    if [[ " ${impl} " == *" ${k} "* || " ${crit} " == *" ${k} "* ]]; then
      fails+="security critic of ${what} ${k}: must differ from the Implementer's and the critic's"$'\n'
    fi
  done
  for role in critic security-critic; do
    count="$(awk -F '\t' -v r="${role}" '$2 == r && $5 != "reject"' "${log}" | grep -c . || true)"
    ((count <= rounds)) || fails+="${role}: ${count} rounds, more than ${rounds}: stop, the human decides"$'\n'
  done
  for role in challenger critic security-critic; do
    open="$(open_blockers "${role}")"
    if ((open > 0)); then
      fails+="${role}: ${open} open BLOCKER(s): a later round of the same slug closes them only after a fix (a writing launch; a Proposer launch for Challengers), else reject each with proof naming the slug that raised it"$'\n'
    fi
  done
  count="$(launches)"
  ((count <= budget)) || fails+="launches: ${count}, budget ${budget}: stop, the human decides"$'\n'
  n_debt="$(awk -F '\t' -v r="${run}" '!/^#/ && $2 == r' "${debt}" | grep -c . || true)"
  n_logged="$(awk -F '\t' 'NF == 7 && $5 != "reject" && $6 ~ /^debt: /' "${log}" | grep -c . || true)"
  if ((n_debt < n_logged)); then
    fails+="debt.tsv has ${n_debt} row(s) for run ${run}, its log ${n_logged} --debt launch(es): restore the removed rows"$'\n'
  fi
  unchecked="$(awk -F '\t' 'NF == 6 && $5 != "reject"' "${log}" | grep -c . || true)"
  fails="$(printf '%s' "${fails}" | awk 'NF')"
  if [[ -n "${fails}" ]]; then
    printf 'gate %s: FAIL (class %s, profile %s)\n%s\n' "${run}" "${eff_class}" "${eff_profile}" "${fails}" >&2
    exit 1
  fi
  if ((n_debt > 0)); then
    printf 'gate %s: pass with model debt (class %s, profile %s, %s launches): %s substitute(s) in .agents/harness/debt.tsv for the human to revisit\n' \
      "${run}" "${eff_class}" "${eff_profile}" "${count}" "${n_debt}"
  else
    printf 'gate %s: pass (class %s, profile %s, %s launches)\n' "${run}" "${eff_class}" "${eff_profile}" "${count}"
  fi
  if [[ ! -d "${dir}/tables" ]]; then printf 'note: run started before table snapshots, judged by the current tables\n'; fi
  if ((unchecked > 0)); then printf 'note: %s launch(es) recorded before --model: their model was not checked\n' "${unchecked}"; fi
}

# yaml_str text: text as a double-quoted YAML scalar.
yaml_str() { printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"; }

# generate dest: every agent file the tables give, under dest. Read-only roles get no shell: their prompt carries
# the diff, and a shell allowlist (git with --output, difftool, ext-diff) still writes.
generate() {
  local dest="$1" t r slug family expect h access desc file
  while IFS=$'\t' read -r t r slug family expect; do
    h="$(handle "${t}" "${r}" "${slug}")"
    access="$(role_field "${r}" 2)"
    desc="$(yaml_str "$(role_field "${r}" 4) Harness role ${r} on ${slug} (${family}); launch it only from a line of scripts/harness.sh plan or pick.")"
    case "${t}" in
      claude) file="${dest}/.claude/agents/${h}.md" ;;
      cursor) file="${dest}/.cursor/agents/${h}.md" ;;
      opencode) file="${dest}/.opencode/agent/${h}.md" ;;
      *) die "no agent directory for tool ${t}" ;;
    esac
    mkdir -p "$(dirname "${file}")"
    {
      printf -- '---\n'
      case "${t}" in
        claude)
          printf 'name: %s\ndescription: %s\n' "${h}" "${desc}"
          if [[ "${access}" == ro ]]; then printf 'tools: Read, Grep, Glob\n'; fi
          printf 'model: %s\n' "${slug}"
          ;;
        cursor)
          printf 'name: %s\ndescription: %s\nmodel: %s\n' "${h}" "${desc}" "${slug}"
          if [[ "${access}" == ro ]]; then printf 'readonly: true\n'; else printf 'readonly: false\n'; fi
          ;;
        opencode)
          printf 'description: %s\nmode: subagent\nmodel: %s\n' "${desc}" "${slug}"
          if [[ "${access}" == ro ]]; then
            printf 'permission:\n  edit: deny\n  bash: deny\n  webfetch: deny\n  task: deny\n'
          fi
          ;;
      esac
      printf -- '---\n\n<!-- Generated by scripts/harness.sh sync from .agents/harness/: edit there, then sync. -->\n\n'
      cat "${tables}/roles/${r}.md"
    } >"${file}"
  done <<<"$(entries "${models}")"
}

generated_list() { # dir: the generated agent files under dir, relative, sorted
  (cd "$1" && find .claude/agents .cursor/agents .opencode/agent -maxdepth 1 -name 'harness-*.md' 2>/dev/null | sort)
}

skill_links="multi-agent"
agent_dirs=".claude .claude/agents .claude/skills .cursor .cursor/agents .opencode .opencode/agent"
cmd_sync() {
  local f s d
  check_tables
  for d in ${agent_dirs}; do
    [[ ! -L "${root}/${d}" ]] || die "${d} is a symbolic link: sync writes only into real directories of the repository"
  done
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  generate "${tmp}"
  while IFS= read -r f; do
    if [[ -n "${f}" && ! -f "${tmp}/${f}" ]]; then
      rm -f "${root}/${f}"
      printf 'removed %s\n' "${f}"
    fi
  done <<<"$(generated_list "${root}")"
  while IFS= read -r f; do
    mkdir -p "$(dirname "${root}/${f}")"
    if [[ -L "${root}/${f}" ]] || ! cmp -s "${tmp}/${f}" "${root}/${f}"; then
      rm -f "${root}/${f}"
      cp "${tmp}/${f}" "${root}/${f}"
      printf 'wrote %s\n' "${f}"
    fi
  done <<<"$(generated_list "${tmp}")"
  for s in ${skill_links}; do
    mkdir -p "${root}/.claude/skills"
    if [[ "$(readlink "${root}/.claude/skills/${s}" 2>/dev/null || true)" != "../../.agents/skills/${s}" ]]; then
      [[ -L "${root}/.claude/skills/${s}" || ! -e "${root}/.claude/skills/${s}" ]] ||
        die ".claude/skills/${s} is not a link: move it away, sync replaces only links"
      rm -f "${root}/.claude/skills/${s}"
      ln -s "../../.agents/skills/${s}" "${root}/.claude/skills/${s}"
      printf 'linked .claude/skills/%s\n' "${s}"
    fi
  done
  printf 'sync: done\n'
}

# check_tables: every invariant of the tables, all problems at once.
check_tables() {
  local problems role t need
  problems="$(awk -F '\t' '!/^#/ && NF { if (NF != 2 || ($2 != "model" && $2 != "family")) printf "tools.tsv:%d: not tool<TAB>model|family\n", NR }' "${tools_tsv}")"
  problems+=$'\n'"$(awk -F '\t' '!/^#/ && NF {
    if (NF != 4 || $1 !~ /^[a-z-]+$/ || ($2 != "ro" && $2 != "rw") || ($3 != "findings" && $3 != "done") || $4 == "")
      printf "roles.tsv:%d: not role<TAB>ro|rw<TAB>findings|done<TAB>description\n", NR }' "${roles_tsv}")"
  problems+=$'\n'"$(awk -F '\t' '!/^#/ && NF {
    if (NF != 5 || $3 !~ /^[0-9]+$/ || $4 !~ /^[1-9][0-9]*$/ || $5 !~ /^[1-9][0-9]*$/)
      printf "profiles.tsv:%d: not profile<TAB>roles<TAB>challengers<TAB>max launches<TAB>max critic rounds\n", NR }' "${profiles}")"
  problems+=$'\n'"$(awk -F '\t' -v fams=" ${FAMILIES} " -v rolefile="${roles_tsv}" -v toolfile="${tools_tsv}" '
    BEGIN {
      while ((getline line < rolefile) > 0) if (line !~ /^#/ && line != "") { split(line, c, "\t"); role[c[1]] = 1 }
      while ((getline line < toolfile) > 0) if (line !~ /^#/ && line != "") { split(line, c, "\t"); tool[c[1]] = c[2] }
    }
    /^#/ || $0 == "" { next }
    NF != 5 { printf "models.tsv:%d: not tool<TAB>role<TAB>slug<TAB>family<TAB>expect\n", NR; next }
    !($1 in tool) { printf "models.tsv:%d: unknown tool %s\n", NR, $1 }
    !($2 in role) { printf "models.tsv:%d: unknown role %s\n", NR, $2 }
    index(fams, " " $4 " ") == 0 { printf "models.tsv:%d: unknown family %s\n", NR, $4 }
    $3 !~ /^[A-Za-z0-9][A-Za-z0-9._\/-]*$/ { printf "models.tsv:%d: bad slug %s\n", NR, $3 }
    $1 == "claude" && ($4 != "anthropic" || $3 !~ /^(opus|sonnet|haiku|fable)$/ || $5 !~ /^claude-/) { printf "models.tsv:%d: Claude Code subagents run Anthropic models by alias only (opus, sonnet, haiku, fable), expecting a claude-* id\n", NR }
    $5 !~ /^[A-Za-z0-9._\/:*?-]+$/ || $5 !~ /[A-Za-z0-9][A-Za-z0-9._\/:-]+[A-Za-z0-9]/ || $5 ~ /[*?][*?]/ { printf "models.tsv:%d: bad expect glob %s: a model name with * or ? wildcards\n", NR, $5 }
    seen[$1 FS $2 FS $3]++ { printf "models.tsv:%d: %s/%s lists %s twice\n", NR, $1, $2, $3 }
    { h = tolower($3); sub(/^github-copilot\//, "", h); gsub(/[\/.]/, "-", h); h = $1 FS $2 FS h
      if ((h in hs) && hs[h] != $3) printf "models.tsv:%d: %s and %s give the same agent file\n", NR, hs[h], $3
      hs[h] = $3 }
    { have[$1 FS $2] = 1 }
    END { for (t in tool) for (r in role) if (!have[t FS r]) printf "models.tsv: no %s entry for role %s\n", t, r }
  ' "${models}")"
  while IFS= read -r role; do
    [[ -z "${role}" || -f "${tables}/roles/${role}.md" ]] || problems+=$'\n'"no ${tables#"${root}"/}/roles/${role}.md"
  done <<<"$(entries "${roles_tsv}" | cut -f 1)"
  for role in $(entries "${profiles}" | cut -f 2); do
    is_role "${role}" || problems+=$'\n'"profiles.tsv: unknown role ${role}"
  done
  problems+=$'\n'"$(awk -F '\t' '!/^#/ && NF {
    if (NF != 2 || $1 !~ /^D[012]$/ || $2 == "") printf "classes.tsv:%d: not D0|D1|D2<TAB>glob\n", NR }' "${classes}")"
  if [[ -z "$(printf '%s' "${problems}" | awk 'NF')" ]]; then
    need="$(entries "${profiles}" | awk -F '\t' '$3 > m { m = $3 } END { print m + 0 }')"
    for t in $(entries "${tools_tsv}" | cut -f 1); do
      problems+=$'\n'"$(check_tool_keys "${t}" "${need}" 2>&1)"
    done
  fi
  problems="$(printf '%s' "${problems}" | awk 'NF')"
  [[ -z "${problems}" ]] || die "tables:"$'\n'"${problems}"
}

# check_tool_keys tool need: the tool lists enough distinct keys for need Challengers besides the Proposer's, a
# critic besides the Implementer's and a security critic besides both.
check_tool_keys() {
  local prop impl crit
  pick "$1" proposer 1 "" "" || return 0
  prop="${taken// /}"
  if (($2 > 0)); then pick "$1" challenger "$2" "${prop}" "" || return 0; fi
  pick "$1" implementer 1 "" "" || return 0
  impl="${taken// /}"
  pick "$1" critic 1 "${impl}" "" || return 0
  crit="${taken// /}"
  pick "$1" security-critic 1 "${impl} ${crit}" "" || return 0
}

cmd_check() {
  local drift="" s f
  check_tables
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' EXIT
  generate "${tmp}"
  [[ "$(generated_list "${tmp}")" == "$(generated_list "${root}")" ]] ||
    drift+="agent files missing (<) or extra (>): "$(diff <(generated_list "${tmp}") <(generated_list "${root}") | grep -E '^[<>]' | tr '\n' ' ' || true)$'\n'
  while IFS= read -r f; do
    [[ -z "${f}" ]] || { [[ ! -L "${root}/${f}" ]] && cmp -s "${tmp}/${f}" "${root}/${f}"; } || drift+="stale ${f}"$'\n'
  done <<<"$(generated_list "${tmp}")"
  for s in ${skill_links}; do
    [[ "$(readlink "${root}/.claude/skills/${s}" 2>/dev/null || true)" == "../../.agents/skills/${s}" ]] ||
      drift+="missing link .claude/skills/${s}"$'\n'
  done
  [[ -z "${drift}" ]] || die "generated files out of date, run scripts/harness.sh sync:"$'\n'"${drift}"
  printf 'check: ok\n'
}

(($# > 0)) || usage
mode="$1"
shift
case "${mode}" in
  classify) cmd_classify "$@" ;;
  pick) cmd_pick "$@" ;;
  start) cmd_start "$@" ;;
  plan) (($# == 1)) || usage; cmd_plan "$1" ;;
  record) cmd_record "$@" ;;
  reject) cmd_reject "$@" ;;
  gate) (($# == 1)) || usage; cmd_gate "$1" ;;
  sync) (($# == 0)) || usage; cmd_sync ;;
  check) (($# == 0)) || usage; cmd_check ;;
  *) usage ;;
esac
