# 058 — Enum task fields: workflow-declared value sets, selectable in New task

**Status:** ✅ done (1/1)
**Spec:** amends §8.1.2 (the `enum` type, `values:`, `multiple:`, `default:`,
create-time normalization and required-default substitution, the lane-override
note) and §8.4 (`.Task.Fields` in a preview)
**Issue:** [#245](https://github.com/lezli01/vincent/issues/245)

## Problem

§8.1.2's field vocabulary — `string`, `integer`, `number`, `boolean` — had no
way to say "this value is one of a fixed set". An author who wanted an
environment, a release channel or a target branch picked from three options had
only the regex escape hatch:

```yaml
- name: environment
  type: string
  pattern: '^(dev|staging|prod)$'
```

That validates but does not *publish*. `GET /v1/workflows` hands a client a
pattern, not a list, so nothing can enumerate the members: New task renders a
free-text row, the human has to already know the spelling, and a typo is a 400
after the fact rather than a control that cannot be wrong. The set then gets
restated in the field's `description` prose so it is discoverable at all, and
the prose drifts from the pattern.

Booleans already get a real selector in New task for exactly this reason. A
closed set of strings deserves the same.

## Approach

A fifth type, `enum`, whose members live in `values:` and whose cardinality is
declared per field in `multiple:`, plus a `default:` that belongs to every type.
Task fields stay `map[string]string` in storage, on the wire, in templates, in
branch names and in fan-out inheritance — [022](022-workflow-declared-task-fields.md)
decision 1 is untouched. The daemon stays the authoritative validation boundary
(022 decision 5) and the map stays open (022 decision 3).

022's own decision 2 ("the first type vocabulary is string, integer, number, and
boolean") is a starting vocabulary rather than a closed one: its beat rejects "an
open-ended type name plus a regex for every field" in favour of "a small enum [of
types that] gives the TUI meaningful controls", which is the same argument one
level down. `enum` extends that decision in its own direction rather than
relitigating it.

## Decisions

**1. `default:` and `values:` take native YAML scalars, canonicalized to
strings.** *(2026-08-30)*

`FieldDefinition` decodes into Go strings, so a bare `default: true` on a boolean
field or `default: 3` on an integer field would have been a yaml type error
rather than a schema error, and `values: [1, 2, 3]` would have failed `[]string`
decoding with a yaml message instead of a schema one. Both decode as
`goccy/go-yaml` `ast.Node`s instead, through a `FieldDefinition.UnmarshalYAML`
that takes each scalar's **literal source text** — so `default: 1.50` is `"1.50"`
and not `"1.5"`. An author never has to know the value is a string underneath.

A `multiple: true` enum's `default:` additionally accepts a sequence of scalars
(`default: [dev, prod]`), normalized exactly as a task value is. Everywhere else
a sequence, and anywhere a mapping, is a load error at `fields[i].default`.

The shape errors are *recorded* during decoding and reported by
`validateFieldDefinitions`, not returned from `UnmarshalYAML`: `Parse` turns a
decode error into a single pathless `Error`, and a schema mistake deserves the
same `fields[i].default` source path every other structural mistake carries.

The cost is one duplication — the alias struct inside `UnmarshalYAML` restates
FieldDefinition's yaml keys, because a custom unmarshaler bypasses the outer
decoder's `DisallowUnknownField` and it has to be re-applied. It is guarded by
`TestFieldDefinitionAliasCoversEveryKey`, which builds a document out of every
tagged key and fails if the alias forgot one.

**Beaten:** a `Values`/`Default` pair of custom struct types carrying their own
shape flags. It keeps the decode local, but `field.Default.Text` at every use
site in the API, the CLI and the preview is a worse public shape than a plain
`string` for a feature whose entire point is that the value *is* a string.

**2. The daemon normalizes a `multiple` value on create.** *(2026-08-30)*

`POST /v1/tasks` splits on `,`, trims each element, drops empties, deduplicates,
reorders to declared order and rejoins — then checks membership. `--field
reviewers="cy, ana"` becomes `ana,cy`; `dev,dev` becomes `dev`; a non-member is
still a 400 that names the offending element, and the message is better for
having normalized first. The task row records the canonical string.

This is what the encoding exists for: every client — TUI, `--field`,
`--fields-file`, curl — produces the same value for the same selection, so
template output and branch names are stable. An empty or whitespace-only value is
treated as absent, as for every other type.

It **extends** 022 decision 5 rather than reversing it, and is recorded
explicitly because "the daemon validates, it does not rewrite" is a reasonable
reading of 022 that a reviewer may hold. The daemon remains the one authoritative
gate; it gains one canonicalizing step immediately ahead of it, in
`Workflow.PrepareTaskFields`, and that step is deterministic, idempotent and
loses nothing — an element the declaration does not know survives normalization
in place, precisely so validation can name it.

**3. Only a required field's `default:` is substituted server-side.**
*(2026-08-30)*

When the caller omits a declared **required** field's key, `POST /v1/tasks` fills
its `default:` before normalizing, validating and inserting, so a scripted caller
that omits it no longer gets a 400 and the task row records the value that
actually applied.

An **optional** field's default is published through `GET /v1/workflows` and
seeded by clients — the New task row, on workflow selection — but the daemon
never invents it. An optional field the caller omits therefore stays genuinely
absent from `.Task.Fields`, so `{{ with index .Task.Fields "x" }}` keeps meaning
what it means today, and adding a `default:` to an existing optional field is not
a silent behaviour change for workflows that guard on presence. A key present but
empty is never defaulted, at any requiredness — an explicitly cleared row is a
decision, not an omission.

**Beaten:** substituting every default. It reads more consistent and quietly
converts the presence test §8.4 tells authors to write into a test that is always
true.

**4. Enum and `default:` ship together.** *(2026-08-30)*

One task document, one pull request. They are coupled where it counts: a picker
wants an initial selection, and decision 5's preview rule is one rule for both.

**5. A preview binds a value the workflow could actually receive.**
*(2026-08-30)*

`workflow.previewFields` binds a required field, in order: its `default:` when it
has one; else, for an enum, its first declared value; else the existing
`SentinelField`. `SentinelField("environment")` is `<field.environment>`, which
is by construction not a member of its own enum, so a preview that bound it would
render something the workflow could never receive. Optional fields stay absent,
which is exactly what decision 3 guarantees — the preview keeps binding only what
it can honestly claim.

**6. `multiple:` is enum-only, like `values:`.** *(2026-08-30)*

Not asked, derived from the schema. The issue states the rule for `values:` and
is silent on `multiple:`; "a boolean that accepts more than one boolean" has no
meaning, so declaring it on a `string`, `integer`, `number` or `boolean` field is
a load error. `pattern` alongside `enum` is an error for the same reason from the
other side: the members *are* the constraint.

## Consequences that follow

- **Task [035](035-github-issue-prefill.md)'s prefill needs no change.** It
  offers a value only when `FieldDefinition.Validate` accepts it (035 decision
  7), and that is the routine `ValidateTaskFields` calls, so membership checking
  arrives for free. A prefilled single member is already a canonical `multiple`
  value.
- **Fan-out lane `fields:` overrides are still not validated** against the root
  workflow's declarations (022 decision 4). A lane may bind a non-member to a
  name the root declares as an enum. That is unchanged behaviour, not a
  regression this task introduces, and §8.1.2 now says so, so it is not filed as
  a bug later.
- **Older clients** see an unknown `type: enum`, fall through to a free-text row
  and run no local check; the daemon still gates. Stated in §8.1.2 and in the
  API reference.
- **The built-ins were re-audited** per [037](037-update-workflows-builtin.md)
  decision 5. `create-workflow`'s `workflow_name` is a free string constrained by
  a real pattern (a file name) and no enum fits it; its `global` stays a boolean
  whose description documents "false, or left unset" as meaningful, so giving it
  a `default:` would change what leaving the TUI row alone means. Neither built-in
  changes. `update-workflows`' version-coupled "The bar" checklist gains item 5,
  *Closed sets and defaults* — a workflow feature missing from that list is one
  the built-in will never propagate.

## Work

- [x] **058.1** — `enum` with `values:`/`multiple:`, `default:` on every type,
  create-time normalization and required-default substitution, the preview
  binding rule, the three new wire keys, and the New task value picker.

No gate script: this is a schema and form-contract change with no daemon
end-to-end behaviour a gate would exercise that the API tests do not. The New
task picker is judged by eye, the way M3 and [017](017-workflow-graph.md) are.
