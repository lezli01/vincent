# Task 017 gate — the workflow graph in the TUI

**Acceptance (task 017.8):** every construct the workflow language has draws
correctly, and reads correctly, in a real terminal.

This gate has **no script**, deliberately. What is being judged is whether a
picture is legible — the same judgement `scripts/m3-gate.sh` declines to
automate, seeding a walkthrough instead of asserting. The topology underneath
is already asserted: `internal/tui/workflowgraph` holds ten golden renders plus
geometry invariants (back-edge target, frame containment, rank order, no
overlaps), and a live test drives the real definition endpoint end to end. A
gate script would re-assert that over curl, more slowly, and still not answer
the only open question.

So this document is the **corpus** — workflows to point the TUI at — and the
record of when someone last looked.

## Setup

```sh
go run mage.go build
mkdir -p ~/.config/vincent/workflows        # or the platform's config dir
cp docs/gates/corpus/*.yaml ~/.config/vincent/workflows/   # see below
./bin/vincent                                # then `:` → workflows
```

Every workflow below is written to be *readable*, not runnable: they exist to
be drawn. Put them in the global scope, open the workflows screen, and press
`g` on each.

## The corpus

Save each block as its own file in the workflows directory.

### 1. `seq.yaml` — three ordinary steps

```yaml
name: seq
steps:
  - {id: plan, type: agent, prompt: plan it}
  - {id: build, type: command, run: make build}
  - {id: ship, type: manual, instructions: ship it}
```

**Look for:** one column, top to bottom, a single `END` under the last step.

### 2. `guarded.yaml` — a guard and a check

```yaml
name: guarded
steps:
  - {id: plan, type: agent, prompt: plan it, check: make test}
  - {id: maybe, type: command, run: make deploy, if: '{{ eq .Fields.mode "full" }}'}
  - {id: ship, type: command, run: make ship}
```

**Look for:** `chk` on `plan`, `if` on `maybe`, and — the point — **no second
branch** off `maybe`. False there means skip and carry on. Select `maybe` and
check the strip at the bottom shows the whole expression.

### 3. `gate.yaml` — a condition

```yaml
name: gate
steps:
  - {id: plan, type: agent, prompt: plan it}
  - {id: gate, type: condition, if: '{{ .Fields.deploy }}'}
  - {id: deploy, type: command, run: make deploy}
```

**Look for:** two ways out of `gate`, labelled `true` and `false`, with false
reaching `END` and true continuing to `deploy`.

### 4. `group.yaml` — a parallel group

```yaml
name: group
steps:
  - {id: plan, type: agent, prompt: plan it}
  - id: checks
    type: parallel
    max_parallel: 2
    steps:
      - {id: lint, type: command, run: make lint}
      - {id: unit, type: command, run: make test}
      - {id: e2e, type: command, run: make e2e, if: '{{ .Fields.slow }}'}
  - {id: ship, type: command, run: make ship}
```

**Look for:** a **light** frame, three members side by side in source order,
`max 2` on the header, and **no merge node** — a group's join is its members
finishing. All three converge on `ship`.

### 5. `spread.yaml` — fan-out, a named lane, an agent merge

```yaml
name: spread
steps:
  - {id: plan, type: agent, prompt: plan it}
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: agent, prompt: build the api}
          - {id: api_test, type: command, run: make test-api}
      - {id: web, workflow: seq, if: '{{ .Fields.web }}'}
    merge:
      on_conflict: agent
      agent: {id: fixup, type: agent, prompt: resolve the conflict}
```

**Look for:** a **heavy** frame, lanes captioned `api` and `web if`, the named
lane as **one collapsed box** reading `seq / workflow`, and a `merge` node
*below* the frame carrying an `agent` badge. `enter` on the collapsed lane does
nothing — that is intended in this version.

### 6. `repeat.yaml` — a counted loop

```yaml
name: repeat
steps:
  - {id: plan, type: agent, prompt: plan it}
  - id: repeat
    type: loop
    count: 3
    max_iterations: 5
    steps:
      - {id: work, type: agent, prompt: keep going}
      - {id: verify, type: command, run: make verify}
  - {id: ship, type: command, run: make ship}
```

**Look for:** a **double** frame, `×3 max 5` on the header, a back-edge from
`verify` returning to the header, and the loop's exit leaving from the
**header** — not from the bottom of the body.

### 7. `escape.yaml` — a break

```yaml
name: escape
steps:
  - {id: plan, type: agent, prompt: plan it}
  - id: repeat
    type: loop
    for_each: '{{ .Steps.plan.Result }}'
    steps:
      - {id: work, type: agent, prompt: keep going}
      - {id: enough, type: break, if: '{{ .Steps.work.Success }}'}
  - {id: ship, type: command, run: make ship}
```

**Look for:** `for_each` on the header with no count — how many iterations it
becomes is discovered at run time. `enough`'s `true` edge must reach **`ship`**,
the step after the loop. If it points back at the loop header, that is the one
thing a break means not to happen.

### 8. `nested.yaml` — a condition inside a loop

```yaml
name: nested
steps:
  - id: repeat
    type: loop
    count: 2
    steps:
      - {id: work, type: agent, prompt: work}
      - {id: skip, type: condition, if: '{{ .Steps.work.Success }}'}
      - {id: record, type: command, run: make record}
```

**Look for:** `skip`'s `false` edge going to the **loop header**, not to `END`.
A condition inside a loop body ends that *iteration* — which is what `continue`
means — and drawing it as ending the workflow would be a lie about control flow.

### 9. `wide.yaml` — labels that are not ASCII

```yaml
name: wide
steps:
  - {id: a, name: 実装をレビューしてからマージする, type: agent, prompt: review}
  - {id: b, name: "🚀🚀🚀 deploy 🚀🚀🚀 everything everywhere", type: command, run: make deploy}
```

**Look for:** boxes exactly as wide as every other box, labels truncated with an
ellipsis, and **no column drift** on the rows below them. This is the one that
catches a renderer measuring runes instead of display width.

### 10. Anything larger than your terminal

Use `spread.yaml` in a window about 40 columns wide.

**Look for:** panning on both axes, the selection scrolling itself into view as
you move, and — narrower than about 26 columns — the message that the terminal
is too narrow, rather than a graph flattened into a shape that is not true.

## The manual legs

These cannot be asserted from a test, which is why the gate exists.

| # | Do | Expect |
|---|---|---|
| 1 | Press `g`, then `esc`, then `esc` | Graph, then the registry list with your row still selected, then out of the takeover. One layer per press |
| 2 | With the graph open, press `e`, change a step's `id`, save, quit the editor | The graph redraws by itself, with the same node still selected if it still exists |
| 3 | Break the file in the editor and save | The layer says the workflow no longer parses, and names the finding |
| 4 | Press `g` on an entry the list already marks invalid | A note saying there is no graph to draw — not an empty layer |
| 5 | Press `?` with the graph open | The help lists the graph's keys, not the list's |
| 6 | Set your terminal to a monochrome profile, or `NO_COLOR=1` | Every distinction above is still readable: frame weights, type words, `true`/`false`, the selected node's heavier border |
| 7 | Click a node; scroll the wheel | The click selects it; the wheel scrolls the canvas |

## Runs

| Date | Version | Platform | By | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk. A gate that has never been walked on a platform is not
known to pass there.
