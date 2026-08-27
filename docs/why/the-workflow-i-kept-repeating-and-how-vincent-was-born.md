# The workflow I kept repeating—and how vincent was born

Vincent began when I realized that, in many of my agentic coding scenarios, I
was doing the same work over and over again. The ticket changed, but the process
around it barely did. I was not only asking an agent to solve a problem; I was
manually coordinating a small delivery workflow every time.

A bug report from QA was a typical example. I would give the agent the ticket
and ask it to follow a sequence like this:

1. Read the report and inspect the relevant code to decide whether the issue
   could be valid.
2. If it looked valid, try to reproduce it before changing anything.
3. If the reproduction succeeded, fix the underlying problem and add regression
   tests that proved it would not return unnoticed.
4. Update any documentation affected by the change.
5. Bring the implementation, tests, and documentation back to me for review.
6. After I approved the work, open a pull request, move the ticket to the right
   state, and leave a comment explaining what had been delivered and how QA
   should test it.

Each step was useful. The waste was in having to restate, coordinate, and check
the same sequence for every ticket. I had become the workflow engine: carrying
context from one stage to the next, remembering which conditions should stop the
work, checking whether tests and documentation had been handled, and deciding
when the result was ready to leave the machine.

After repeating this only a few times, the shape of the tool I wanted felt
obvious. I should be able to provide a ticket ID and let a previously defined
workflow handle the routine around it. The system should know the order of the
steps, preserve their outputs, verify what can be verified, and pause when human
judgment is actually needed. Once I had reviewed the result, it should be able to
continue with the delivery and ticket follow-up I had already described.

That was the moment vincent was born.

The important idea was not to make one enormous prompt. It was to turn a proven
way of working into something reusable, visible, and durable. The agent could
focus on the work inside each step. Checks could decide whether objective
requirements passed. A human gate could protect the point of no return. And the
workflow could remember the process so I no longer had to perform it from
scratch.

What started as “I should only have to enter the ticket ID” became the foundation
of vincent: describe the process once, run it repeatedly, keep it inspectable,
and reserve human attention for the decisions that genuinely need it.

---

[Back to Why vincent is awesome](README.md) · [Turn a repeated process into a workflow](../guides/workflows.md)
