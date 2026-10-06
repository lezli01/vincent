# shellcheck shell=bash
# What the CI gate scripts share (#729). Sourced, never run: each gate sets
# ROOT to the repository root and then sources this file.
#
#   source "$ROOT/scripts/lib/gate.sh"
#
# It carries two things, and nothing that makes one gate depend on another —
# every gate still isolates itself (its own mktemp -d, config and data dirs,
# port), which is what lets CI run them side by side in gate-group jobs.

# gate_exe is the suffix go gives a binary on this platform.
gate_exe() {
  if [[ "${OS:-}" == "Windows_NT" ]]; then printf '.exe'; fi
}

# _gate_hostpath and _gate_shellpath convert between Git Bash's /tmp-style
# paths and the C:/... ones native Windows tools (go, vincent) take. Elsewhere
# both are the identity.
_gate_hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}
_gate_shellpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -u "$1"; else printf '%s\n' "$1"; fi
}

# gate_build DIR CMD... puts ./cmd/CMD into DIR for each CMD, named CMD plus
# gate_exe — what `go build -o DIR/ ./cmd/CMD...` writes. With
# VINCENT_GATE_BIN set (CI builds the binaries once per job into it) the
# binaries are copied from there instead; unset, they are built, so a local
# ./scripts/<gate>.sh behaves as it always has.
gate_build() {
  local dir="$1" cmd
  shift
  mkdir -p "$dir"
  if [[ -n "${VINCENT_GATE_BIN:-}" ]]; then
    for cmd in "$@"; do
      cp "$(_gate_shellpath "$VINCENT_GATE_BIN")/$cmd$(gate_exe)" "$dir/" \
        || { echo "GATE FAIL: $cmd$(gate_exe) is not in VINCENT_GATE_BIN ($VINCENT_GATE_BIN)" >&2; exit 1; }
    done
    return 0
  fi
  local pkgs=()
  for cmd in "$@"; do pkgs+=("./cmd/$cmd"); done
  (cd "$ROOT" && go build -o "$(_gate_hostpath "$dir")/" "${pkgs[@]}")
}

# gate_build_as FILE CMD puts ./cmd/CMD at FILE, under that exact name — how
# the fakegh gates install the fake as `gh`. VINCENT_GATE_BIN as gate_build.
gate_build_as() {
  local out="$1" cmd="$2"
  mkdir -p "$(dirname "$out")"
  if [[ -n "${VINCENT_GATE_BIN:-}" ]]; then
    cp "$(_gate_shellpath "$VINCENT_GATE_BIN")/$cmd$(gate_exe)" "$out" \
      || { echo "GATE FAIL: $cmd$(gate_exe) is not in VINCENT_GATE_BIN ($VINCENT_GATE_BIN)" >&2; exit 1; }
    return 0
  fi
  (cd "$ROOT" && go build -o "$(_gate_hostpath "$out")" "./cmd/$cmd")
}

# GATE_POLL is how long a wait loop sleeps between looks. A wait — a loop that
# polls the API or the filesystem and leaves as soon as its condition holds —
# used to look once a second, so every gate paid up to a second per wait for
# nothing. Deliberate delays (a debounce, a timer that must elapse, "nothing
# happened for N s") and every `sleep` inside a workflow `run:` body are not
# waits and do not use it.
# shellcheck disable=SC2034 # read by the gates that source this file
GATE_POLL=0.2

# gate_ticks SECS is how many GATE_POLL ticks fill SECS seconds, so a wait's
# wall-clock budget is still written in seconds at its call site:
#
#   for _ in $(seq 1 "$(gate_ticks 30)"); do ...; sleep "$GATE_POLL"; done
gate_ticks() {
  echo $(($1 * 5)) # 5 = 1 / GATE_POLL; bash arithmetic has no fractions
}
