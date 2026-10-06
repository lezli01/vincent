#!/usr/bin/env bash
# Prove `go run mage.go testci` (#736, #728) — the target CI's test step
# runs — against tests that fail on purpose, in a throwaway module. Scenarios
# 1–4 pin VINCENT_TEST_RACE=all, so they prove the same thing on every host:
#
#   1. a test that fails once and then passes is rerun, the run exits 0, the
#      job summary names it, and junit.xml and test.json are written, with an
#      Elapsed for it in test.json
#   2. a test that fails every attempt runs three times (one run, two
#      reruns), fails the target, and is still named in the summary
#   3. a clean run exits 0 and the summary says no test needed a rerun
#   4. a test the race detector fails is never rerun, so a race that would not
#      recur on a rerun still fails the target
#   5. with VINCENT_TEST_RACE=none the race detector is really off: scenario
#      4's racy test passes, the target exits 0, and an earlier run's race
#      reports are not left beside this run's
#   6. with VINCENT_TEST_RACE naming one package of two, the race pass over
#      that package catches its race without a rerun, the other package's
#      test runs exactly once (in the plain pass), the two passes run at the
#      same time, and both passes' reports — test.json and test-race.json —
#      are written beside the summary
#
# Together 4–6 prove each scope the target defaults to: all (linux), none
# (darwin) and a package list (windows).
#
# The target runs the throwaway module through VINCENT_TEST_DIR and writes its
# reports to VINCENT_TEST_REPORT_DIR, with GITHUB_STEP_SUMMARY pointed at a
# temp file. Each test counts its attempts by appending a byte to a file named
# by FLAKE_ATTEMPTS, since a rerun is a fresh process.
#
# Not wired into CI: it proves the target, and the target itself then runs on
# all three platforms in ci.yml's `ci` job, each with its default scope. VINCENT_GATE_SCENARIO=N runs one
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

run_target() { # run_target [SCOPE] — the target's exit code, its output on stdout
  local rc=0
  (cd "$ROOT" && FLAKE_ATTEMPTS="$(hostpath "$ATTEMPTS")" \
    VINCENT_TEST_RACE="${1:-all}" \
    VINCENT_TEST_DIR="$(hostpath "$MOD")" \
    VINCENT_TEST_REPORT_DIR="$(hostpath "$REPORT")" \
    GITHUB_STEP_SUMMARY="$(hostpath "$SUMMARY")" \
    "$MAGE" testci) || rc=$?
  return "$rc"
}

attempts() { wc -c <"$ATTEMPTS" | tr -d ' \r'; }

meet() { # meet FILE PACKAGE MINE THEIRS — a TestMeet that marks MINE, then waits for THEIRS
  cat >"$1" <<EOF
package $2

import (
	"os"
	"testing"
	"time"
)

func TestMeet(t *testing.T) {
	base := os.Getenv("FLAKE_ATTEMPTS")
	if err := os.WriteFile(base+".$3", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(base + ".$4"); err == nil {
			return
		}
	}
	t.Fatal("the $4 pass never started while the $3 pass ran: the passes ran in series")
}
EOF
}

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
  setup s4 "$RACY"
  if run_target; then
    fail "target exited 0 on a test the race detector failed"
  fi
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1 (a race is not rerun)"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test was rerun, and the run failed' <<<"$summary" || fail "summary does not say the failed run was not rerun: $summary"
}

RACY='if n == 1 { x := 0; done := make(chan struct{}); go func() { x++; close(done) }(); x++; <-done; _ = x }'

scenario_5() {
  echo "== scenario 5: VINCENT_TEST_RACE=none runs without the race detector"
  setup s5 "$RACY"
  # An earlier run's race-pass reports, which this run must not leave
  # beside its own.
  mkdir -p "$REPORT"
  for f in test-race.json junit-race.xml reruns-race.txt; do echo stale >"$REPORT/$f"; done
  run_target none || fail "target exited $? on a racy test with the race detector off"
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json"
  local f
  for f in test-race.json junit-race.xml reruns-race.txt; do
    [[ ! -e "$REPORT/$f" ]] || fail "$f is left over from an earlier run, or a race pass ran with VINCENT_TEST_RACE=none"
  done
}

scenario_6() {
  echo "== scenario 6: a package list races only the listed packages"
  setup s6 "$RACY"
  # A second package beside the racy one, counting its own runs.
  mkdir -p "$MOD/steady"
  cat >"$MOD/steady/steady_test.go" <<'EOF'
package steady

import (
	"os"
	"testing"
)

func TestOnce(t *testing.T) {
	f, err := os.OpenFile(os.Getenv("FLAKE_ATTEMPTS")+".steady", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
}
EOF
  # One test in each pass, each waiting for the other to have started: run
  # in series, the first pass's waits out its budget and fails.
  meet "$MOD/steady/meet_test.go" steady plain race
  meet "$MOD/meet_test.go" flake race plain
  if run_target .; then
    fail "target exited 0 on a race in a listed package"
  fi
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1 (a race is not rerun)"
  local steady
  steady="$(wc -c <"$ATTEMPTS.steady" | tr -d ' \r')"
  [[ "$steady" == 1 ]] || fail "steady.TestOnce ran $steady times, want 1 (plain pass only)"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json from the plain pass"
  [[ -s "$REPORT/test-race.json" ]] || fail "no test-race.json from the race pass"
  [[ -s "$REPORT/junit-race.xml" ]] || fail "no junit-race.xml from the race pass"
  local plain raced
  plain="$(jq -r 'select(.Action == "pass" and .Test != null) | .Package + "." + .Test' "$REPORT/test.json" | tr -d '\r')"
  raced="$(jq -r 'select(.Test != null) | .Package' "$REPORT/test-race.json" | tr -d '\r' | sort -u)"
  grep -qx 'flake/steady.TestOnce' <<<"$plain" || fail "the plain pass did not run steady.TestOnce: $plain"
  grep -qx 'flake/steady.TestMeet' <<<"$plain" || fail "the plain pass never met the race pass: $plain"
  local met
  met="$(jq -r 'select(.Action == "pass" and .Test == "TestMeet") | .Package' "$REPORT/test-race.json" | tr -d '\r')"
  [[ "$met" == flake ]] || fail "the race pass never met the plain pass: $met"
  [[ "$raced" == flake ]] || fail "the race pass ran packages other than flake: $raced"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test was rerun, and the run failed' <<<"$summary" || fail "summary does not say the failed run was not rerun: $summary"
}

for n in 1 2 3 4 5 6; do
  if [[ -z "$ONLY" || "$ONLY" == "$n" ]]; then
    "scenario_$n"
  fi
done
echo "== PASS"
