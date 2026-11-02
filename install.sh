#!/bin/sh
# Installs lazyrex from a GitHub release:
#   curl -fsSL https://raw.githubusercontent.com/rbobillot/lazyrex/main/install.sh | sh
# Only function definitions sit above the last line, which calls main inside braces: every truncated download
# either ends before that line or leaves the braces open, a syntax error, so it runs nothing.
set -eu

repo=https://github.com/rbobillot/lazyrex

say() { printf 'lazyrex-install: %s\n' "$*"; }
die() { printf 'lazyrex-install: %s\n' "$*" >&2; exit 1; }
fetch() { curl --proto =https --proto-redir =https --tlsv1.2 -fsSL -o "$1" "$2"; }

usage() {
  printf '%s\n' \
    "usage: curl -fsSL https://raw.githubusercontent.com/rbobillot/lazyrex/main/install.sh | sh" \
    "  LAZYREX_VERSION      release tag to install, such as v0.1.0 (default: the latest release)" \
    "  LAZYREX_INSTALL_DIR  absolute directory to install lazyrex in (default: ~/.local/bin)"
}

is_num() {
  case $1 in
  0) return 0 ;;
  '' | 0* | *[!0-9]*) return 1 ;;
  esac
}

# is_tag accepts what release.yml accepts: vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, N from 1.
is_tag() {
  case $1 in v*) ;; *) return 1 ;; esac
  t=${1#v}
  case $t in
  *-rc.*) core=${t%%-rc.*} rc=${t#*-rc.}; is_num "$rc" && [ "$rc" != 0 ] || return 1 ;;
  *) core=$t ;;
  esac
  case $core in *.*.*.*) return 1 ;; *.*.*) ;; *) return 1 ;; esac
  r=${core#*.}
  is_num "${core%%.*}" && is_num "${r%%.*}" && is_num "${r#*.}"
}

main() {
  if [ $# -gt 0 ]; then
    case $1 in -h | --help) usage; exit 0 ;; esac
    die "unexpected argument $1: set LAZYREX_VERSION or LAZYREX_INSTALL_DIR instead (see --help)"
  fi
  build="build it with go install github.com/rbobillot/lazyrex/cmd/lazyrex@latest"

  kernel=$(uname -s) || die "uname -s failed"
  machine=$(uname -m) || die "uname -m failed"
  case $kernel in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN* | Windows_NT) die "no lazyrex binary for Windows: $build" ;;
  *) die "no lazyrex binary for $kernel (Linux and macOS only): $build" ;;
  esac
  case $machine in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "no lazyrex binary for $os/$machine (amd64 and arm64 only): $build" ;;
  esac
  # hw.optional.arm64 is 1 on Apple silicon, even in a shell running under Rosetta.
  if [ "$os/$arch" = darwin/amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || :)" = 1 ]; then
    arch=arm64
  fi

  version=${LAZYREX_VERSION:-}
  case $version in '' | v*) ;; *) version=v$version ;; esac
  if [ -n "$version" ] && ! is_tag "$version"; then
    die "LAZYREX_VERSION $LAZYREX_VERSION is not vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N"
  fi

  dir=${LAZYREX_INSTALL_DIR:-}
  if [ -z "$dir" ]; then
    [ -n "${HOME:-}" ] || die "HOME is not set: set LAZYREX_INSTALL_DIR"
    dir=$HOME/.local/bin
  fi
  case $dir in /*) ;; *) die "LAZYREX_INSTALL_DIR $dir is not an absolute path" ;; esac
  while [ "$dir" != / ] && [ "${dir%/}" != "$dir" ]; do dir=${dir%/}; done

  command -v curl >/dev/null 2>&1 || die "curl is not installed"
  if command -v sha256sum >/dev/null 2>&1; then hasher=sha256sum
  elif command -v shasum >/dev/null 2>&1; then hasher='shasum -a 256'
  else die "neither sha256sum nor shasum is installed: cannot check the download"
  fi

  staged=""
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/lazyrex-install.XXXXXX") || die "could not create a temporary directory"
  # set -e stays in force inside the trap: each removal must not stop the next one.
  trap '[ -z "$staged" ] || rm -f "$staged" || :; rm -rf "$tmp" || :' EXIT
  trap 'exit 1' HUP INT TERM

  if [ -n "$version" ]; then sums=$repo/releases/download/$version/SHA256SUMS label=$version
  else sums=$repo/releases/latest/download/SHA256SUMS label="the latest release"
  fi
  fetch "$tmp/SHA256SUMS" "$sums" || die "could not download $sums"
  suffix=_${os}_${arch}.tar.gz
  n=0 name="" want=""
  while IFS= read -r line || [ -n "$line" ]; do
    file=${line#*  }
    [ "$file" != "$line" ] || continue
    case $file in lazyrex_*"$suffix") ;; *) continue ;; esac
    if [ -n "$version" ] && [ "$file" != "lazyrex_${version#v}$suffix" ]; then continue; fi
    n=$((n + 1)) name=$file want=${line%%  *}
  done <"$tmp/SHA256SUMS"
  [ "$n" = 1 ] || die "SHA256SUMS of $label lists $n archives for $os/$arch, want 1"
  v=${name#lazyrex_} && v=${v%"$suffix"}
  is_tag "v$v" || die "SHA256SUMS of $label names an archive of a bad version: $name"
  tag=v$v
  case $want in *[!0-9a-f]*) want="" ;; esac
  [ ${#want} = 64 ] || die "SHA256SUMS of $tag has no valid checksum for $name"

  url=$repo/releases/download/$tag/$name
  say "downloading lazyrex $tag for $os/$arch"
  fetch "$tmp/$name" "$url" || die "could not download $url"
  got=$($hasher <"$tmp/$name") || die "$hasher failed"
  [ "${got%% *}" = "$want" ] || die "$name does not match SHA256SUMS of $tag: nothing installed"

  top=${name%.tar.gz}
  bad="$name holds no file $top/lazyrex, or more than one"
  # A second member of the same name would replace the first, or write through it, when extracted.
  members=$(tar -tzf "$tmp/$name" "$top/lazyrex") || die "could not extract $top/lazyrex from $name"
  [ "$members" = "$top/lazyrex" ] || die "$bad"
  # In an empty directory, a hard link has no target to point to.
  mkdir "$tmp/extract" || die "could not create $tmp/extract"
  tar -xzf "$tmp/$name" -C "$tmp/extract" "$top/lazyrex" || die "could not extract $top/lazyrex from $name"
  bin=$tmp/extract/$top/lazyrex
  [ -f "$bin" ] && [ ! -h "$bin" ] || die "$bad"

  hint="choose a directory you own with LAZYREX_INSTALL_DIR (this script never uses sudo)"
  mkdir -p "$dir" || die "cannot create $dir: $hint"
  [ ! -d "$dir/lazyrex" ] || die "$dir/lazyrex is a directory"
  staged=$(mktemp "$dir/.lazyrex.XXXXXX" 2>/dev/null) || die "cannot write to $dir: $hint"
  cp "$bin" "$staged" && chmod 0755 "$staged" && mv -f "$staged" "$dir/lazyrex" ||
    die "cannot write to $dir: $hint"
  staged=""

  say "installed lazyrex $tag ($os/$arch) to $dir/lazyrex"
  case ":${PATH:-}:" in
  *":$dir:"*) ;;
  *) say "$dir is not in PATH: add it, for example with export PATH=\"$dir:\$PATH\" in your shell profile" ;;
  esac
  found=$(command -v lazyrex 2>/dev/null || :)
  if [ -n "$found" ] && [ "$found" != "$dir/lazyrex" ]; then
    say "$found comes first in PATH: it is not the lazyrex just installed"
  fi
  command -v rex >/dev/null 2>&1 || say "rex is not in PATH: lazyrex needs it (API revision 9)"
}

{ main "$@"; }
