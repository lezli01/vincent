---
title: Handing work off
---

# Handing work off

An issue's tasks are its **workshop**: they do the work, one run after another,
in the issue's main worktree. What they produce still has to go somewhere — a
pull request, a merge, a comment on the issue, a message to a team — and that
step is a **handoff**. One integration task claims several finished issues and
runs a workflow you choose over all of them, then records what became of each.

This page tells that story end to end: what a run delivers, when an issue is
ready, how a handoff claims it, the four outcomes, the marker a board shows,
and the grant that lets a handoff write to GitHub. The fields and routes are in
the [API reference](../reference/api.md#handoffs) and the
[CLI reference](../reference/cli.md#vincent-handoff); this page is the story
behind them. A handoff is not
[`vincent chat handoff`](../reference/cli.md#vincent-chat-handoff), which turns
a chat into a task.

## Deliveries

A task's **delivery** is what its run produced. It has a **kind**:

- **`change`**, the default — commits on the task's branch, to be integrated.
- **`reply`** — an answer, a refusal or questions, to be posted where the work
  came from. This is how a workshop ends in words: "needs info", "won't fix".
- **`none`** — the run produced nothing to hand on.

A step records one while the task runs, the way it reports its status:

```sh
vincent task deliver --title "Retry the upload on a 503" --body-file pr.md --summary-file brief.md
```

It takes a title and a Markdown body (what a pull request or a comment would
say), a summary, the commit the run verified (`--head`; the branch tip by
default), a suggested close reason for the issue and the proof. The same call
is `POST /v1/tasks/{id}/delivery` over the API and `task_deliver` over
[MCP](mcp.md), which is the route a step in a container uses.

You rarely need it for a plain change. A task that reaches `done` without
recording a delivery gets an **implicit** one: `change` when its branch holds
commits its base does not, `none` otherwise, titled and described by the task.
A later call replaces the delivery, a follow-up that re-runs a finished task
clears it, and a merge-back task delivers its side task's delivery — so the
delivery always describes the run that last finished. Each change emits
`task.delivery_changed`.

See [Deliveries](../reference/api.md#deliveries) for every field and
[`vincent task deliver`](../reference/cli.md#vincent-task-deliver) for the
flags.

## Ready to hand off

An issue moves to the [`hand_off` lane](issues.md#lanes) when nothing of it is
running and its latest finished main-line delivery is **ready**. The workflow
decides that at `→ done`, with its
[`hand_off`](../reference/workflow-schema.md#hand_off) key:

- **`auto`**, the default, marks the delivery ready — the issue reaches
  `hand_off` as soon as its work finishes.
- **`explicit`** marks it not ready, unless a step ran
  `vincent task deliver --ready`. Use it for a spike or a research workflow
  whose result a person judges first, or one that decides in a step.

An in-progress issue whose finished work is not ready is **idle**: nothing is
running and nothing moves on its own. A person decides with
`vincent issue ready <id>`, which moves it to `hand_off`, or
`vincent issue unready <id>`, which takes a hand-off issue back to
`in_progress` (`.` on its card in the TUI does either). Both are local
writes, never sent to GitHub. See
[Ready to hand off](../reference/api.md#ready-to-hand-off) for what each
refuses.

## Starting a handoff

A handoff runs a workflow that declares
[`role: handoff`](../reference/workflow-schema.md#role). Nothing else runs
one, and a handoff runs nothing else: there is no default handoff workflow.
Name the issues, or let the handoff choose them:

```sh
vincent handoff start --project 1 --workflow merge-train 12 14 15
vincent handoff start --project 1 --workflow merge-train --delivery change --label ready-to-ship --max 8
```

A selection takes the eligible issues — open, in `hand_off`, claimed by no
other handoff, and with a delivery not already delivered — most urgent first,
then the oldest delivery. One call creates the handoff, its task and every
claim, or nothing: an id that is not eligible, or a selection that finds
nothing, creates no task. `POST /v1/handoffs` is the same over the API.

The handoff's task is an ordinary root task with no issue, cut from the
project's base branch. Its steps read the claimed items from
[`.Handoff`](../reference/workflow-schema.md#handoff) or from the file at
[`$VINCENT_HANDOFF_FILE`](../reference/workflow-schema.md#the-handoffs-items) —
the handoff's id, its grant, and per item the issue, its delivery, and the
delivering task's id, branch and verified head.

**A claim is exclusive.** While a handoff claims an issue, it belongs to that
handoff: a new task on it, a chat hand-off or a follow-up is refused
`issue_in_handoff`, and only the handoff's own task may add work to it.

`vincent handoff ls` lists handoffs (`--issue` finds every one that claimed an
issue), and `vincent handoff show <id>` prints one with its items. A handoff
is `live` while its task runs, `waiting` once the task has settled with an item
still waiting, and `settled` otherwise.

## Outcomes

Each claimed issue ends with one of four outcomes, recorded by a verb — run by
the handoff's own steps, or by you:

| Verb | Outcome | What happens to the issue |
|---|---|---|
| `vincent handoff deliver` | `delivered` | Closed with `--close` — the delivery's suggested reason, else `completed`. `--close none` leaves it open, kept out of later handoffs until new work replaces the delivery |
| `vincent handoff return` | `returned` | The claim is dropped and its readiness withdrawn, so it waits in `in_progress`. `--follow-up` queues a prompt on the issue's main task; with `--side`, as a new side task merged back into the main branch |
| `vincent handoff wait` | `waiting` | Parked on something outside the handoff. It keeps its claim after the task settles, and closing the issue — by anyone, a GitHub import included — records it delivered |
| `vincent handoff release` | `released` | The claim is dropped with no verdict; the issue stays eligible for a later handoff |

Each verb takes the handoff, then one issue id, several, or `--all` (every
unresolved item; waiting ones are left alone). `--note` is kept on the item,
and `--comment-file` posts a comment on each issue before anything else is
written to it:

```sh
vincent handoff deliver 3 12 14 --close completed
vincent handoff wait 3 15 --note "waiting on the vendor's fix"
vincent handoff return 3 15 --follow-up "Rebase onto the new API and retry"
```

A call is **all or nothing**: one refused item records nothing for any of
them. An item already resolved with a different outcome is refused
`handoff_item_resolved`; repeating an item's own outcome succeeds and changes
nothing — its comment and close included — so a retried step converges rather
than posting twice. When the handoff's task settles, every item still
unresolved is released. See [Item outcomes](../reference/api.md#item-outcomes).

## The handoff marker

Beside its lane, an issue carries a **handoff marker** that says where it
stands toward a handoff:

- **`pending`** — in `hand_off`, unclaimed: a handoff may claim it now.
- **`claimed`** — a handoff holds it and has not decided it. It shows in any
  lane, because the handoff's task may restart work on it.
- **`waiting`** — a handoff holds it, parked with `handoff wait`.
- **`delivered`** — delivered and left open, until new work makes it
  `pending` again.
- **`idle`** — in progress, with finished work that is not ready.

While the claiming handoff's task is paused, a `claimed` or `waiting` marker
says so (`paused` on the wire). `vincent issue ls` prints the marker in the
`LANE` cell and `--handoff pending` filters on it; the API spells it `handoff`
on each issue and takes `?handoff=`. The full rule is the
[handoff marker](../reference/api.md#handoff-marker).

In the TUI, the [issues board](tui.md#handing-issues-off--space-v-i) shows the
marker on each card. `space` selects a pending card, `V` every pending card,
and `I` opens the handoff form for the selection: the `role: handoff` workflow,
the grant, a title and whether to start paused.

![The handoff form over pending platform-infra issue #16: the handoff-train
workflow, seven grant capabilities all unticked, an optional title and the
start-paused switch](../assets/tui-handoff-form.png)

An issue's detail names the handoff that holds it, and the handoff's task
lists its items with their outcomes:

![Issue #14's detail in the hand-off lane, its Handoff section naming handoff
H1, live on task #20, and the item parked waiting by a
person](../assets/tui-issue-handoff.png)

![Handoff task #20 waiting at its review gate, its Items block listing three
issues: #13 delivered, #14 waiting and #15 still
unresolved](../assets/tui-handoff-task.png)

## Grants: letting a handoff write to GitHub

A workflow step that writes to GitHub through vincent — `vincent github pr
create`, `merge`, `close`, `reopen`, `comment` or `rerun`, or a close or a
comment on an imported issue — is refused `409 forge_write_needs_human`.
Nothing is sent. A **grant** is how you say yes in advance, for one handoff.
It names capabilities from a closed list:

| Capability | Lets the handoff's task |
|---|---|
| `issue.comment` | Comment on its imported issues |
| `issue.close` | Close its imported issues |
| `pull.create` | Push an item's branch and open its pull request |
| `pull.comment` | Comment on its pull requests |
| `pull.merge` | Merge its pull requests |
| `pull.close` | Close its pull requests |
| `checks.rerun` | Rerun its pull requests' checks |

A granted write is admitted only from the handoff's **own task**, and only on
the handoff's own items and pull requests. It is made as `handoff`, not as
you, and queued for GitHub like any other write vincent makes. Every other
step — another task, a fan-out lane — is still refused.

**Only a person gives a grant.** An agent may start a handoff, but one that
asks for a grant is refused `grant_needs_human`. A person consents in one of
three places:

- **On the command line**, with `vincent handoff start --grant
  pull.create,issue.close` — your own command.
- **In the TUI form**, by ticking capabilities — they all start off — and
  submitting with `ctrl+s`. The keypress is the consent.
- **On a trigger**, by arming it: a trigger's
  [`start_handoff`](triggers.md#start_handoff-start-a-handoff) action carries
  a `grant:`, and arming the trigger with it is the consent for every handoff
  it starts. Adding a capability counts as arming, so it asks again
  ([configuration](../reference/configuration.md#triggers) has the rule).

The [security model](../security-model.md#what-vincent-writes-outside-its-own-directories)
says what this check is and is not: it keeps an agent's honest call off
GitHub, but it is not a boundary against an agent running as you.

> **Breaking, from the release after v0.11.0.** An ungranted `vincent github
> pr` write from a workflow step or a chat agent used to be accepted as yours;
> it is now refused `forge_write_needs_human`. A step's own `gh pr …` in its
> worktree is unaffected. See the [changelog](../../CHANGELOG.md).

## Worked lifecycles

Each of these ships as a workflow you install with `vincent workflow init
<name> --from <name>`, and an unarmed trigger under
[`docs/examples/triggers/`](../examples/triggers/) that starts it. Copy the
trigger to `{config_dir}/triggers/`, set its project, and arm it yourself. The
workflow files' headers walk through each decision.

### A merge train

[`merge-train`](../../examples/merge-train.yaml) stacks every delivered
change into the handoff task's own branch — each item's verified `head_sha`
replayed in order with `git rebase --onto` — verifies them together, opens one
pull request, merges it pinned to the verified commit, and only then runs
`vincent handoff deliver --all`. Its
[trigger](../examples/triggers/merge-train.yaml) runs at 02:00 on weekdays and grants
`pull.create`, `pull.merge` and `issue.close`. A conflict fails the step and
leaves it to a person; the items release when the task settles, so nothing is
lost.

This repository lands its own work that way:
[`github-handoff-train`](../../.vincent/workflows/github-handoff-train.yaml)
is the merge train with a review and a repair loop, started by
[its own trigger](../examples/triggers/github-handoff-train.yaml).

### Comment and close

[`comment-and-close`](../../examples/comment-and-close.yaml) is for work that
ends in words. It takes the `reply` deliveries, posts each one's body on its
issue with `vincent handoff deliver --comment-file`, and closes it — without a
single agent session. Anything that is not a reply is released with a note.
Its [trigger](../examples/triggers/comment-and-close.yaml) grants
`issue.comment` and `issue.close`.

### A pull request per issue

[`pr-per-issue`](../../examples/pr-per-issue.yaml) opens one pull request per
item, for the item's own delivering task, then marks each item `wait`. Review
and merge stay yours: when the pull request merges, GitHub's `Closes #N`
closes the issue, sync imports that, and the close records the item
delivered. It is one command step over `$VINCENT_HANDOFF_FILE`, not a
fan-out, because the grant admits only the handoff's own task and a lane is a
task of its own. Its [trigger](../examples/triggers/pr-per-issue.yaml) fires on
each issue reaching `hand_off` and grants `pull.create`.

### Tell someone, or deploy

[`notify-handoff`](../../examples/notify-handoff.yaml) posts the handoff file
to a webhook and delivers every item with `--close none`, so each issue stays
open for whoever picks it up. It writes nothing to GitHub, so its
[trigger](../examples/triggers/notify-handoff.yaml) grants nothing. A deploy is
the same shape — run the script, and deliver only if it succeeded:

```yaml
name: deploy-handoff
description: Deploy the handed-off changes, then record them delivered.
role: handoff
platforms: [posix]

steps:
  - id: deploy
    type: command
    run: |
      set -e
      ./scripts/deploy.sh
      vincent handoff deliver {{.Handoff.ID}} --all --close none
```

### Needs info

A workshop that cannot proceed without the reporter can end in questions: a
step delivers `kind: reply` with them as the body,

```sh
vincent task deliver --kind reply --title "Two questions before this can be fixed" --body-file questions.md
```

and a handoff granted `issue.comment` posts them and parks the issue:

```sh
vincent handoff wait "$h" "$issue" --comment-file questions.md --note "asked the reporter"
```

**Nothing wakes the item when the reporter answers.** A trigger cannot fire on
an issue comment today — the `issues` source's events are `opened`, `closed`,
`reopened`, `labeled`, `unlabeled`, `lane_changed` and `handoff_changed`. Once
the answer is in, a person sends the issue back to its workshop:

```sh
vincent handoff return 3 15 --follow-up "The reporter answered; read the thread and fix it"
```

If the reporter closes the issue instead, the close records the item
delivered.
