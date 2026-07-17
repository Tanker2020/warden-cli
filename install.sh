#!/usr/bin/env bash
# install.sh — build and install the `warden` CLI.
#
#   ./install.sh                # install to ~/.local/bin (default)
#   WARDEN_INSTALL_DIR=/usr/local/bin ./install.sh
#
# What it does, in order:
#   1. Finds a Go toolchain — or downloads one privately to ~/.warden/toolchain
#      (nothing system-wide, no sudo) if none is installed.
#   2. Downloads module dependencies.
#   3. Builds the warden binary and installs it to the install dir.
#   4. Ensures the install dir is on PATH (appends one line to your shell rc
#      only if it isn't already).
#
# Requirements: bash, curl (or wget), tar; macOS or Linux. Until the sac-lang
# language front-end is published, the build needs it as a sibling checkout
# (../sac-lang) via a local `replace` — the script checks and says so if it's
# missing. Once sac-lang is published and the replace is removed, no sibling is
# needed.

set -euo pipefail

# ---------- pretty printing ----------
say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- locations ----------
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${WARDEN_INSTALL_DIR:-$HOME/.local/bin}"
TOOLCHAIN_DIR="$HOME/.warden/toolchain"
GO_VERSION="${WARDEN_GO_VERSION:-1.25.1}"   # used only when Go isn't installed
MIN_GO_MINOR=24                              # go.mod says go 1.24

# ---------- sanity: language front-end (sac-lang) ----------
# The CLI embeds the public sac-lang front-end (parser + classifier). Until it's
# published to github.com/Tanker2020/sac-lang and go.mod's local `replace` is
# removed, sac-lang must be checked out as a sibling directory (../sac-lang).
# Once published + replace removed, this whole check goes away — a clean clone
# builds with no sibling repos.
if grep -q '^replace github.com/Tanker2020/sac-lang' "$REPO_DIR/go.mod" 2>/dev/null; then
  if [ ! -d "$REPO_DIR/../sac-lang" ]; then
    die "expected the sac-lang module next to this one (../sac-lang).
       go.mod still has a local replace for it (not yet published). Clone
       github.com/Tanker2020/sac-lang as a sibling directory and re-run — or,
       once it's published, delete the replace line in go.mod."
  fi
fi

# ---------- fetch helper (curl or wget) ----------
fetch() { # fetch <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "need curl or wget to download Go"
  fi
}

# ---------- 1. find or fetch Go ----------
go_ok() { # go_ok <go-binary> → 0 if version >= 1.MIN_GO_MINOR
  local v
  v="$("$1" version 2>/dev/null | sed -n 's/.*go1\.\([0-9]*\).*/\1/p')" || return 1
  [ -n "$v" ] && [ "$v" -ge "$MIN_GO_MINOR" ]
}

GO_BIN=""
if command -v go >/dev/null 2>&1 && go_ok "$(command -v go)"; then
  GO_BIN="$(command -v go)"
  say "using installed Go: $("$GO_BIN" version)"
elif [ -x "$TOOLCHAIN_DIR/go/bin/go" ] && go_ok "$TOOLCHAIN_DIR/go/bin/go"; then
  GO_BIN="$TOOLCHAIN_DIR/go/bin/go"
  say "using previously downloaded Go: $("$GO_BIN" version)"
else
  case "$(uname -s)" in
    Darwin) GOOS=darwin ;;
    Linux)  GOOS=linux ;;
    *) die "unsupported OS: $(uname -s) (install Go >= 1.$MIN_GO_MINOR manually, then re-run)" ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) GOARCH=arm64 ;;
    x86_64|amd64)  GOARCH=amd64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
  TARBALL="go${GO_VERSION}.${GOOS}-${GOARCH}.tar.gz"
  say "Go not found — downloading go${GO_VERSION} (${GOOS}/${GOARCH}) to $TOOLCHAIN_DIR (private to warden, no sudo)"
  mkdir -p "$TOOLCHAIN_DIR"
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  fetch "https://go.dev/dl/${TARBALL}" "$TMP/$TARBALL"
  rm -rf "$TOOLCHAIN_DIR/go"
  tar -C "$TOOLCHAIN_DIR" -xzf "$TMP/$TARBALL"
  GO_BIN="$TOOLCHAIN_DIR/go/bin/go"
  go_ok "$GO_BIN" || die "downloaded Go failed its version check"
  say "downloaded $("$GO_BIN" version)"
fi

# ---------- 2. dependencies ----------
say "downloading module dependencies"
(cd "$REPO_DIR" && "$GO_BIN" mod download)

# ---------- 3. build + install ----------
say "building warden"
mkdir -p "$INSTALL_DIR"
(cd "$REPO_DIR" && "$GO_BIN" build -trimpath -o "$INSTALL_DIR/warden" ./cmd/warden)
say "installed $("$INSTALL_DIR/warden" --version) → $INSTALL_DIR/warden"

# ---------- 4. PATH ----------
case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    say "$INSTALL_DIR is already on your PATH — you're done"
    ;;
  *)
    # pick the rc file for the user's login shell
    case "${SHELL:-}" in
      */zsh)  RC="$HOME/.zshrc" ;;
      */bash) RC="$HOME/.bashrc" ;;
      *)      RC="$HOME/.profile" ;;
    esac
    PATH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
    if [ -f "$RC" ] && grep -qsF "$PATH_LINE" "$RC"; then
      say "PATH line already in $RC — restart your shell to pick it up"
    else
      printf '\n# added by warden install.sh\n%s\n' "$PATH_LINE" >> "$RC"
      say "added $INSTALL_DIR to PATH in $RC"
    fi
    warn "open a new terminal (or: source $RC) before running warden"
    ;;
esac

echo
echo "Next steps:"
echo "  warden login       # authenticate (nyxtra.dev; --issuer http://localhost:5173 for local dev)"
echo "  warden deploy --file <rule>.sac"
