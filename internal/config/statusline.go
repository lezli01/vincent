package config

import (
	"fmt"
	"slices"
	"strings"
	"text/template/parse"
)

// checkStatusLineFields walks a parsed `tui.chat.status_line` and checks every
// field it names, wherever it names it (review F5 of task 170).
//
// Executing the template against a zero ChatStatusFields is not enough:
// text/template resolves a field only when it evaluates it, so a misspelled
// field inside `{{if .Cost}}…{{end}}` is never reached with an empty cost and
// would pass the load, only to break the header once a turn reported one.
//
// Where dot is the chat's fields — the top level, an `if`'s branches, and a
// `with` or `range` pipeline and its `else` — a field must be one of
// ChatStatusFields and must not be dereferenced further, since every one is
// a string or an int. Inside a `with` or `range` body dot is one of those
// values, which has no fields at all. `$` is always the chat's fields.
func checkStatusLineFields(root parse.Node) error {
	var walk func(n parse.Node, dotIsFields bool) error
	field := func(ident []string, dotIsFields bool) error {
		name := "." + strings.Join(ident, ".")
		if !dotIsFields {
			return fmt.Errorf("can't evaluate field %s: inside with or range, dot is a value, not the chat's fields", name)
		}
		if !slices.Contains(chatStatusFieldNames, "."+ident[0]) {
			return fmt.Errorf("can't evaluate field %s", ident[0])
		}
		if len(ident) > 1 {
			return fmt.Errorf("can't evaluate field %s of .%s, which is a value", ident[1], ident[0])
		}
		return nil
	}
	walk = func(n parse.Node, dotIsFields bool) error {
		switch n := n.(type) {
		case nil:
			return nil
		case *parse.ListNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Nodes {
				if err := walk(c, dotIsFields); err != nil {
					return err
				}
			}
		case *parse.ActionNode:
			return walk(n.Pipe, dotIsFields)
		case *parse.PipeNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Cmds {
				if err := walk(c, dotIsFields); err != nil {
					return err
				}
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				if err := walk(a, dotIsFields); err != nil {
					return err
				}
			}
		case *parse.FieldNode:
			return field(n.Ident, dotIsFields)
		case *parse.VariableNode:
			if len(n.Ident) > 1 && n.Ident[0] == "$" {
				return field(n.Ident[1:], true)
			}
		case *parse.ChainNode:
			return walk(n.Node, dotIsFields)
		case *parse.IfNode:
			return walkBranch(walk, &n.BranchNode, dotIsFields, dotIsFields)
		case *parse.WithNode:
			return walkBranch(walk, &n.BranchNode, dotIsFields, false)
		case *parse.RangeNode:
			return walkBranch(walk, &n.BranchNode, dotIsFields, false)
		case *parse.TemplateNode:
			return walk(n.Pipe, dotIsFields)
		}
		return nil
	}
	return walk(root, true)
}

// walkBranch walks an if, with or range: its pipeline and else branch where
// dot is what it was, its body where dot is bodyDot.
func walkBranch(walk func(parse.Node, bool) error, b *parse.BranchNode, dot, bodyDot bool) error {
	if err := walk(b.Pipe, dot); err != nil {
		return err
	}
	if err := walk(b.List, bodyDot); err != nil {
		return err
	}
	if b.ElseList != nil {
		return walk(b.ElseList, dot)
	}
	return nil
}
