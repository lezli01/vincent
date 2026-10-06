#!/usr/bin/env bash
# Prove `go run mage.go testraceci` (#736) — the target CI's test step runs —
# against tests that fail on purpose, in a throwaway module:
#
#   1. a test that fails once and then passes is rerun, the run exits 0, the
#      job summary names it, and junit.xml and test.json are written, with an
#      Elapsed for it in test.json
#   2. a test that fails every attempt runs three times (one run, two
#      reruns), fails the target, and is still named in the summary
#   3. a clean run exits 0 and the summary says no test needed a rerun
#   4. a test the race detector fails is never rerun, so a race that would not
#      recur on a rerun still fails the target
#
# The target runs the throwaway module through VINCENT_TEST_DIR and writes its
# reports to VINCENT_TEST_REPORT_DIR, with GITHUB_STEP_SUMMARY pointed at a
# temp file. Each test counts its attempts by appending a byte to a file named
# by FLAKE_ATTEMPTS, since a rerun is a fresh process.
#
# Not wired into CI: it proves the target, and the target itself then runs on
# all three platforms in ci.yml's `ci` job. VINCENT_GATE_SCENARIO=N runs one
# scenario.
#
# Requirements: bash, go (with cgo and a C compiler, for -race), jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "CHECK FAIL: $*" >&2; exit 1; }

cleanup() {
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# Build mage's binary once, so each scenario's exit code is the target's.
echo "== compile mage"
MAGE="$TMP/mage"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  MAGE+=".exe"
fi
(cd "$ROOT" && go run mage.go -compile "$(hostpath "$MAGE")")

MOD="" REPORT="" SUMMARY="" ATTEMPTS=""
setup() { # setup NAME TEST_BODY — a module with one TestFlake plus a passing TestSteady
  local dir="$TMP/$1"
  MOD="$dir/mod" REPORT="$dir/report" SUMMARY="$dir/summary.md" ATTEMPTS="$dir/attempts"
  mkdir -p "$MOD"
  printf 'module flake\n\ngo 1.22\n' >"$MOD/go.mod"
  cat >"$MOD/flake_test.go" <<EOF
package flake

import (
	"os"
	"testing"
)

// attempt records one more run of TestFlake and returns how many there
// have been, this one included.
func attempt(t *testing.T) int {
	t.Helper()
	f, err := os.OpenFile(os.Getenv("FLAKE_ATTEMPTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Size())
}

func TestFlake(t *testing.T) {
	n := attempt(t)
	$2
}

func TestSteady(t *testing.T) {}
EOF
}

run_target() { # run_target — the target's exit code, its output on stdout
  local rc=0
  (cd "$ROOT" && FLAKE_ATTEMPTS="$(hostpath "$ATTEMPTS")" \
    VINCENT_TEST_DIR="$(hostpath "$MOD")" \
    VINCENT_TEST_REPORT_DIR="$(hostpath "$REPORT")" \
    GITHUB_STEP_SUMMARY="$(hostpath "$SUMMARY")" \
    "$MAGE" testraceci) || rc=$?
  return "$rc"
}

attempts() { wc -c <"$ATTEMPTS" | tr -d ' \r'; }

scenario_1() {
  echo "== scenario 1: a test that fails once passes on a rerun"
  setup s1 'if n == 1 { t.Fatal("first attempt fails on purpose") }'
  run_target || fail "target exited $? on a test that passed on its rerun"
  [[ "$(attempts)" == 2 ]] || fail "TestFlake ran $(attempts) times, want 2"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -q '^### Tests rerun' <<<"$summary" || fail "no Tests rerun section: $summary"
  grep -qF 'flake.TestFlake: 2 runs, 1 failures' <<<"$summary" || fail "summary does not name TestFlake: $summary"
  [[ -s "$REPORT/junit.xml" ]] || fail "no junit.xml"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json"
  local elapsed
  elapsed="$(jq -r 'select(.Test == "TestFlake" and .Action == "pass") | .Elapsed' "$REPORT/test.json" | tr -d '\r')"
  [[ -n "$elapsed" ]] || fail "test.json has no Elapsed for TestFlake's passing run"
  local junit
  junit="$(tr -d '\r' <"$REPORT/junit.xml")"
  grep -qF 'name="TestFlake"' <<<"$junit" || fail "junit.xml has no TestFlake case"
}

scenario_2() {
  echo "== scenario 2: a test that fails every attempt fails the target"
  setup s2 't.Fatalf("attempt %d fails on purpose", n)'
  if run_target; then
    fail "target exited 0 on a test that failed every attempt"
  fi
  [[ "$(attempts)" == 3 ]] || fail "TestFlake ran $(attempts) times, want 3 (one run, two reruns)"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'flake.TestFlake: 3 runs, 3 failures' <<<"$summary" || fail "summary does not name TestFlake: $summary"
}

scenario_3() {
  echo "== scenario 3: a clean run reruns nothing"
  setup s3 '_ = n'
  run_target || fail "target exited $? on a clean run"
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test needed a rerun.' <<<"$summary" || fail "summary does not say nothing was rerun: $summary"
}

scenario_4() {
  echo "== scenario 4: a data race is never rerun"
  # Races on its first attempt only, so a rerun would pass.
  setup s4 'if n == 1 { x := 0; done := make(chan struct{}); go func() { x++; close(done) }(); x++; <-done; _ = x }'
  if run_target; then
    fail "target exited 0 on a test the race detector failed"
  fi
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1 (a race is not rerun)"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test was rerun, and the run failed' <<<"$summary" || fail "summary does not say the failed run was not rerun: $summary"
}

for n in 1 2 3 4; do
  if [[ -z "$ONLY" || "$ONLY" == "$n" ]]; then
    "scenario_$n"
  fi
done
echo "== PASS"
