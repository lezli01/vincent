# “Done” is not a success condition

One of the least useful things an agent can tell me is that it is done.

The statement may be sincere, and the implementation may even look convincing,
but neither makes it correct. The code might not compile on another platform. A
test may have been forgotten. The reported bug may still reproduce through a
slightly different path. If the same model that performed the work is also the
only judge of that work, confidence can increase without evidence increasing at
all.

That is why vincent separates doing the work from deciding whether the work met
an objective condition.

An agent or command step can declare a `check`. After the step body finishes,
vincent runs that check in the same worktree. The step advances only when the
body and the check both succeed. For a bug fix, the check might run the focused
regression test. For a documentation change, it might validate links. For a
generated artifact, it might compare a schema or inspect the expected files.

This changes the meaning of a workflow. The prompt describes the desired
outcome, but the check defines the evidence required to continue.

Consider the difference:

> Fix the issue and make sure the tests pass.

versus a workflow that asks an agent to implement the fix and then executes the
repository's test command itself. In the first version, “tests pass” is part of
the agent's interpretation and report. In the second, it is an observed process
with an exit code and captured output.

Failure becomes more useful too. A failed check marks the attempt with
`check_failed`, records its output, and supplies the failure to a retry. The next
attempt does not have to guess why the previous one was rejected. It receives
the concrete evidence and can correct the work. Retries are bounded, so a bad
assumption cannot turn into an endless loop of optimistic answers.

Checks do not replace judgment. A command can prove that tests pass, but it
cannot always prove that an API feels right, a migration is acceptable, or a
user-facing explanation is clear. Those decisions belong at a human gate. The
important part is that objective facts are checked objectively, leaving people
to review the things that genuinely require people.

This is one of the foundations of trustworthy agentic work: let the agent create,
let deterministic checks verify, and do not confuse a confident final message
with a success condition.

---

[Back to Why vincent is awesome](README.md) · [Use workflow checks](../guides/workflows.md#7-verification-with-check)
