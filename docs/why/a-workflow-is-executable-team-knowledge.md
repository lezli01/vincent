# A workflow is executable team knowledge

A good prompt can solve one task. A good workflow can preserve how a team solves
that kind of task.

Many engineering playbooks live in fragile places: one person's memory, a copied
chat message, or a checklist that gradually stops matching the repository. Even
when the process is sound, the next person has to translate the prose back into
commands, decisions, and handoffs. The knowledge exists, but it is not
executable.

vincent workflows turn that playbook into versioned YAML. A project workflow can
live under `.vincent/workflows/` beside the code it serves. It can declare typed
task inputs, run deterministic commands, call agents for reasoning, verify
results with checks, branch through native control flow, and pause at human
gates. The process can be reviewed with the same discipline as any other change
to the repository.

That makes assumptions visible. If a bug-fix workflow requires an issue number,
the field is declared. If publishing needs approval, the gate is in the file. If
tests must pass, the check is not left to memory. If a command is platform
specific, the workflow says so. Future readers do not have to infer the policy
from a prompt that happened to work once.

The three workflow scopes support different kinds of knowledge. Project
workflows travel with one repository. Global workflows capture a personal or
machine-wide routine that applies across projects. Built-ins provide a stable
starting point. A narrower scope can override a broader one without making the
selection ambiguous.

Tasks snapshot their workflow when they are created. Editing the YAML improves
future tasks without rewriting a run already in progress. The daemon reloads
valid changes on save and keeps the previous loaded version when an edit is
invalid. That gives workflow evolution the stability expected from executable
configuration.

The portable [vincent Workflows skill](../../skills/vincent-workflows/SKILL.md)
helps turn an intended process into the smallest reliable workflow. Its most
important contribution is not generating YAML syntax; it asks which decisions
matter, chooses deterministic primitives first, places gates around external
effects, and validates against the installed binary.

Once the playbook is executable, consistency no longer depends on who remembers
the steps. The team can improve the process once and reuse the improvement every
time the workflow runs.

---

[Back to Why vincent is awesome](README.md) · [Write a reusable workflow](../guides/workflows.md)
