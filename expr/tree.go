package expr

import _ "embed"

// Tree returns the parsed expression as plain JSON-ready data, for evaluators
// outside Go (the browser rules runtime). Each node is an array:
//
//	["lit", value]  ["id", name]  ["un", op, x]  ["bin", op, l, r]  ["call", fn, args...]
//
// Evaluators must follow the semantics in the package comment; Conformance
// holds cases every evaluator is tested against.
func (e *Expr) Tree() any { return tree(e.root) }

func tree(n node) any {
	switch x := n.(type) {
	case lit:
		return []any{"lit", x.v}
	case ident:
		return []any{"id", string(x)}
	case unary:
		return []any{"un", x.op, tree(x.x)}
	case binary:
		return []any{"bin", x.op, tree(x.l), tree(x.r)}
	case call:
		out := []any{"call", x.fn}
		for _, a := range x.args {
			out = append(out, tree(a))
		}
		return out
	}
	return nil
}

// Conformance is a JSON list of {"expr", "env", "want"} cases: evaluating expr
// with names from env (absent names are unknown) must give want. Go and every
// other evaluator run the same cases so they cannot drift apart.
//
//go:embed conformance.json
var Conformance []byte
