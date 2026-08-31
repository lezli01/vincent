# 072 — Chats on codex and cursor

**Status:** ✅ done (1/1)
**Issue:** #283
**Closes:** [063](063-free-chat.md) decision 3's deferral, and §20's chat entry
for codex and cursor

Task 063 shipped chats on claude only and refused codex and cursor at creation
with `agent_cannot_resume`. That refusal was never a judgement that those CLIs
cannot resume — it was that the capability had not been *proved*. Every record
that touched it deferred rather than closed the question, and each named the
same unblocking condition: a fixture captured against a named CLI build.

- **063 decision 3** — "codex `exec resume <thread_id>` and cursor `--resume`
  are follow-up work, each landing with a fixture captured against a named CLI
  version."
- **§20's chat entry** — names both, "until then those adapters are *refused*".
- **§9.3 / §9.7 "Cannot resume"** — "no fixture captured against a named codex
  build proves the argv", "Deferred to §20 with the fixture requirement
  attached."

The condition is met: `codex-cli 0.150.1` and `cursor-agent
2026.08.11-e8db854`, both captured for this work. What is **not** reopened:
replaying the conversation as prompt context stays rejected, `agent.Resumer`
stays an optional interface rather than an `Adapter` method (§9.1), and the
`agent_cannot_resume` path is kept rather than deleted.

## Decisions

**1. A resumed codex run is full-auto, and that is made unreachable rather than
merely unreached.** `codex exec resume` carries `--json`, `-m`, `-c` and
`--dangerously-bypass-approvals-and-sandbox`, but no `-s/--sandbox` at all, so
`restricted` has no argv spelling on it. The adapter does not invent one: a
resumed run always passes the bypass flag, and §9.3 states that positively the
way every other codex limitation is stated.

Silently dropping a restriction is the one outcome worth guarding, so the guard
is structural rather than a run-time check. `POST /v1/chats` hardcodes
`PermissionMode: full_auto` and has no request field to override it — the
decoder rejects unknown fields, so there is nothing to ask with — and nothing
else in the codebase sets `RunSpec.ResumeSessionID`. `TestChatsAreAlwaysFullAuto`
in `internal/api` asserts both halves. The day chats gain a permission mode, it
fails, and this is reopened deliberately instead of being discovered as an
escalation in the field. `-c sandbox_mode="workspace-write"` was considered and
rejected: more capture work than an unreachable branch is worth.

**2. The classifier is `session_lost`, on codex only, and nothing else.** codex
gains a `failure.go` recognizing exactly one condition, matched **only** when
the run actually passed a resume id, so no workflow step can be misdiagnosed
(§9.2's rule, verbatim). `usage_limit` and `unauthenticated` stay unclassified
on both adapters: an account cannot be made to hit its quota on demand, so
those wordings still have no capturable fixture and task 003's decision still
holds for them. `TestQuotaAndAuthStopsStayUnclassified` passes untouched on
both — its scenarios pass no resume id — and its doc comment's "this adapter
recognizes nothing" was amended rather than left to become false.

cursor gets **no** `failure.go`, and for a stronger reason than a missing
fixture. The capture (`resume_unknown_2026.08.11.jsonl`) shows cursor-agent
does not refuse an unknown `--resume` id at all: it starts a fresh chat under
that id, stamps it on every line and exits 0. There is no refusal to
recognize, so matching one would be a guess about a CLI that answered. The
brief anticipated a `failure.go` here; the capture said otherwise and the
capture wins, which is exactly the "provisional until pinned by fixtures" rule
this work was run under.

**3. cursor's stream id *is* the id `--resume` accepts, so no `SessionCreator`
was built.** This was the single largest risk in the issue and the one thing
that made cursor contingent: every line of the existing cursor fixture carries
`session_id`, but the capture scrubbed it, so nothing on disk showed the
stream's id was the resume key. A two-turn capture settled it — turn 2 resumed
turn 1's `session_id` and answered from it. The contingent optional
`agent.SessionCreator` interface, and `create-chat`, are therefore not built:
no core change at all, which is the evidence the optional-interface choice in
§9.1 was right.

**4. The prompt on a resumed codex run is the literal `-`.** `codex exec
[PROMPT]` reads stdin when the prompt argument is absent, which is why a fresh
run passes none. `codex exec resume [SESSION_ID] [PROMPT]` does **not**: its
help says stdin is read only "if `-` is used". So the resumed argv is `exec
resume --json <flags> <id> -`, and `buildArgs` grows positionals it has never
had. Getting this wrong hangs the child on a stdin nobody reads until the step
timeout, which is why it is a decision and not an implementation detail.

**5. The refusal is proven against a stub, and the gate loses its refusal
leg.** `internal/api/chats_test.go` and `internal/chatrun/runner_test.go` used
codex and cursor as their non-resuming examples, and would now assert the
opposite of the truth. Both re-point at `agenttest.StubNonResuming` — placed in
`internal/agent/agenttest` because both packages need it. That is not just a
fix: a refusal pinned to whichever CLI happens to lack a capability today will
keep inverting itself, and a stub cannot.

`scripts/m14-gate.sh`'s refusal leg goes, because after this no shipped adapter
is refused and a real daemon has no subject that can reach it. Registering a
test-only non-resuming adapter in the daemon was rejected — shipping a fake
adapter in the production registry cuts against the same §9.1 property this
exists to protect — and re-pointing the leg at an unknown agent name was
rejected because that asserts `validation_failed`, a different code and a
different contract. The gate instead walks the two-turn continuity check on all
three adapters.

## What shipped

- `internal/agent/codex` — `buildArgs` changes shape for a resumed run;
  `stream.go` gains `threadIDOf` and an explicit `thread.started` case;
  `failure.go` is new; `SupportsResume()` is true; `testedVersions` gains
  `0.150.1`. Fixtures: `resume_0.150.1.jsonl`, `resume_lost_0.150.1.txt`.
- `internal/agent/cursor` — `buildArgs` appends `--resume <id>` last (an
  optional-value flag must have nothing after it to swallow, which is safe only
  because the prompt is piped); `stream.go` gains `sessionIDOf`;
  `SupportsResume()` is true. Fixtures: `resume_2026.08.11.jsonl`,
  `resume_unknown_2026.08.11.jsonl`.
- **No core change.** `internal/api/chats.go`, `GET /v1/agents`'
  `supports_resume` and the TUI's `resumableAgents` all read `agent.CanResume`
  and were not touched.
- `cmd/fakeagent` — the session store is dialect-aware: codex's `exec resume
  <id> -`, cursor's `--resume <id>`, each with the lost-session behaviour its
  real CLI has (codex refuses in codex's captured wording; cursor adopts the
  id). `codexMain` and `cursorMain` gained the `openSession`/`rememberPrompt`/
  `recallLine` calls only `claudeMain` made, and codex's hardcoded
  `"thread_id": "fake-thread-1"` is now the minted id.
- `scripts/m14-gate.sh` — refusal leg out, two-turn walk on all three adapters.
- Spec §9.1, §9.3, §9.7, §20 and decision row 29 amended in place, dated;
  `docs/features.md`, `docs/guides/agents.md`, `docs/guides/tui.md`,
  `docs/reference/api.md` and `docs/reference/cli.md` updated.

## Out of scope, stated

Neither `codex exec` nor `cursor-agent -p` has a mid-run input channel
(`InputSupport: InputNever`, §9.3/§9.7), so a chat on either never enters
`awaiting_input`: no mid-run questions, no permission prompts, and the chat
answer route does nothing for it. Documented positively like every other
missing capability; explicitly not worked around, and not a blocker on the chat
being useful.
