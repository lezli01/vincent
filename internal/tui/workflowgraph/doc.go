// Package workflowgraph draws a workflow definition as a control-flow graph
// for the TUI (task 017, spec §15 view 5).
//
// The workflows screen explains a workflow as a numbered list of its
// top-level steps, which was enough while workflows were linear. The language
// now has structure — `parallel` groups (§7.5), `fan_out` lanes and their
// merge (§7.6), guards and `condition` steps (§7.7), `loop` and `break`
// (§7.8) — and a list can name those constructs without showing their control
// flow.
//
// # Three stages, three files
//
// Build, Layout and Render are separate on purpose (task 017 decision 3):
//
//		definition ──Build──▶ Diagram ──Layout──▶ Scene ──Render──▶ cells
//
//	  - diagram.go turns the API's definition into semantic nodes, edges and
//	    groups. No coordinates.
//	  - layout.go assigns deterministic positions and orthogonal routes. No
//	    styles, no terminal.
//	  - render.go paints a Scene. No topology decisions.
//
// diagram.go and layout.go import neither Bubble Tea nor Lip Gloss, which is
// what makes topology and geometry testable without terminal events and
// reusable by a future builder's command model. render.go may style; model.go
// owns the Bubble Tea component.
//
// # Invariants worth keeping
//
//   - Node identity is semantic and never positional, so a re-layout after a
//     live registry reload keeps a selection where it was. Synthetic nodes —
//     the END, a fan_out's merge, a collapsed workflow reference — carry a `#`
//     prefix so they cannot collide with an authored step id, which the
//     workflow language does not restrict.
//   - Exactly one END terminates the top-level sequence. Nested sequences get
//     none: a `condition` inside a loop body ends that iteration and routes to
//     the loop header, and a `break` routes to whatever follows the loop.
//   - A `parallel` group has no merge node — its join is its members
//     finishing. A `fan_out` does, because the join is a git merge that runs
//     and can block.
//   - Topology survives having every style stripped. Box shapes, frame
//     weights, type words and the `true`/`false` labels carry the meaning;
//     color only reinforces it.
//   - Equal input gives equal output, at every stage.
//
// This first version is a viewer. Its boundaries are shaped so that adding
// mutation later means adding commands around this pipeline rather than
// replacing it (decision 9).
package workflowgraph
