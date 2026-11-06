package runtime

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// envOf exposes a record's slots to expressions, bare or under prefix.
// Several records can be layered: the first one that knows a name wins.
type layer struct {
	prefix string
	rec    *Record
	vals   map[string]any
}

func envOf(layers ...layer) expr.Env {
	return func(name string) (any, bool) {
		for _, l := range layers {
			n := name
			if l.prefix != "" {
				p := l.prefix + "."
				if !strings.HasPrefix(name, p) {
					continue
				}
				n = name[len(p):]
			} else if strings.Contains(name, ".") {
				continue
			}
			if l.rec != nil {
				if n == "id" && l.rec.Class.PKSlot() != nil {
					return float64(l.rec.ID()), true
				}
				if l.rec.Class.Slot(n) != nil {
					return expr.Normalize(l.rec.Values[n]), true
				}
			}
			if l.vals != nil {
				if v, ok := l.vals[n]; ok {
					return expr.Normalize(v), true
				}
			}
			if l.rec == nil && l.vals == nil {
				return nil, true // an absent optional record (e.g. no owner): every name is null
			}
		}
		return nil, false
	}
}

// ComputedValue is one evaluated computed field.
type ComputedValue struct {
	Name, Label, Text string
	Value             any
}

// Computed evaluates the class's computed fields for rec, skipping those
// whose condition fails or whose value is null.
func Computed(rec *Record) []ComputedValue {
	var out []ComputedValue
	env := envOf(layer{rec: rec})
	for _, cp := range rec.Class.Computed {
		if cp.When != nil {
			ok, err := cp.When.Eval(env)
			if err != nil || !expr.Truthy(ok) {
				continue
			}
		}
		v, err := cp.Expr.Eval(env)
		if err != nil || v == nil {
			continue
		}
		var text string
		switch cp.Format {
		case "duration":
			f, ok := v.(float64)
			if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
				continue
			}
			text = expr.FormatDuration(int64(math.Round(f)))
		case "number":
			f, ok := v.(float64)
			if !ok {
				continue
			}
			text = strconv.FormatFloat(f, 'f', cp.Decimals, 64)
		default:
			text = expr.Format(v)
		}
		out = append(out, ComputedValue{Name: cp.Name, Label: firstNonEmpty(cp.Label, cp.Name), Text: text + cp.Suffix, Value: v})
	}
	return out
}

// CalendarText is the text for rec in calendar views: the class's
// calendar_label when it evaluates to a value, else the record title.
func CalendarText(rec *Record) string {
	if e := rec.Class.CalendarLabel; e != nil {
		if v, err := e.Eval(ownEnv(rec)); err == nil && v != nil {
			if s := expr.Format(v); s != "" {
				return s
			}
		}
	}
	return rec.Title()
}

// AddAllowed reports whether a has_many's add_when holds for the parent row.
func AddAllowed(rel *model.Relationship, parent *Record) bool {
	if rel.AddWhen == nil {
		return true
	}
	v, err := rel.AddWhen.Eval(envOf(layer{rec: parent}))
	return err == nil && expr.Truthy(v)
}

type triggerDepthKey struct{}

// runTriggers applies the class's triggers after rec was saved.
func (r *Registry) runTriggers(ctx context.Context, rec *Record) error {
	depth, _ := ctx.Value(triggerDepthKey{}).(int)
	if depth >= 4 || len(rec.Class.Triggers) == 0 {
		return nil
	}
	ctx = context.WithValue(ctx, triggerDepthKey{}, depth+1)
	h, err := r.Class(rec.Class.Name)
	if err != nil {
		return err
	}
	for _, tr := range rec.Class.Triggers {
		if rec.Int(tr.Via) == 0 {
			continue
		}
		target, err := h.Parent(ctx, rec, tr.Via)
		if err != nil {
			continue // dangling or missing link: nothing to update
		}
		env := envOf(layer{prefix: "self", rec: rec}, layer{prefix: "target", rec: target})
		if tr.If != nil {
			ok, err := tr.If.Eval(env)
			if err != nil {
				return fmt.Errorf("trigger %s.%s: %w", rec.Class.Name, tr.Via, err)
			}
			if !expr.Truthy(ok) {
				continue
			}
		}
		changed := false
		for _, a := range tr.Set {
			v, err := a.Expr.Eval(env)
			if err != nil {
				return fmt.Errorf("trigger %s.%s set %s: %w", rec.Class.Name, tr.Via, a.Slot, err)
			}
			if err := target.Set(a.Slot, exprToSlot(target.Class.Slot(a.Slot), v)); err != nil {
				return fmt.Errorf("trigger %s.%s set %s: %w", rec.Class.Name, tr.Via, a.Slot, err)
			}
			changed = true
		}
		if changed {
			th, err := r.Class(target.Class.Name)
			if err != nil {
				return err
			}
			if err := th.Save(ctx, target); err != nil {
				return fmt.Errorf("trigger %s.%s: %w", rec.Class.Name, tr.Via, err)
			}
		}
	}
	return nil
}

// exprToSlot adapts an expression value to what Coerce expects for the slot.
func exprToSlot(s *model.Slot, v any) any {
	f, ok := v.(float64)
	if !ok || s == nil {
		return v
	}
	switch s.Type {
	case "integer", "duration":
		return int64(math.Round(f))
	case "string", "text", "enum":
		return expr.Format(f)
	}
	return f
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
