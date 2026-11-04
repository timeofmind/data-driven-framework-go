package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// ImportSource is one parsed item from an uploaded file: named fields, and
// named lists of child items (e.g. "laps"). The format reader that produced
// it is outside the framework; the class's import block maps it to slots.
type ImportSource struct {
	Fields   map[string]any
	Children map[string][]map[string]any
}

// ImportOutcome reports what Import did.
type ImportOutcome struct {
	Record    *Record   // the new record (nil when Duplicate is set)
	Duplicate *Record   // an existing record with the same dedupe value
	Linked    []*Record // rows the record was linked to (import.link)
	Created   []*Record // rows created in other classes (import.create)
}

// ImportTimezone evaluates the class's import timezone for owner p (e.g.
// "owner.timezone"), falling back to def, then UTC.
func (r *Registry) ImportTimezone(ctx context.Context, c *model.Class, p *Principal, def string) *time.Location {
	name := def
	if c.Import != nil && c.Import.Timezone != nil {
		if v, err := c.Import.Timezone.Eval(r.ownerEnv(ctx, p)); err == nil && expr.Format(v) != "" {
			name = expr.Format(v)
		}
	}
	if loc, err := time.LoadLocation(name); err == nil && name != "" {
		return loc
	}
	return time.UTC
}

func (r *Registry) ownerEnv(ctx context.Context, p *Principal) expr.Env {
	var owner *Record
	if h, err := r.AuthClass(); err == nil && p != nil {
		owner, _ = h.Load(ctx, p.ID)
	}
	return envOf(layer{prefix: "owner", rec: owner})
}

// Import creates a record of h's class for owner p from src, following the
// class's import block: dedupe, field mapping, links, children, and records
// created in other classes. Owner slots are always set to p, links only reach
// rows p may read (and, for owned classes, rows p owns).
func (h *Handle) Import(ctx context.Context, p *Principal, src ImportSource) (*ImportOutcome, error) {
	c := h.Class
	im := c.Import
	if im == nil {
		return nil, fmt.Errorf("%s has no import block", c.Name)
	}
	if p == nil {
		return nil, errors.New("import needs a principal")
	}
	r := h.reg
	ownerEnv := r.ownerEnv(ctx, p)
	fileEnv := func(fields map[string]any, extra ...layer) expr.Env {
		base := append([]layer{{vals: fields}}, extra...)
		inner := envOf(base...)
		return func(n string) (any, bool) {
			if v, ok := inner(n); ok {
				return v, true
			}
			if v, ok := ownerEnv(n); ok {
				return v, true
			}
			if len(n) > 0 && !containsDot(n) {
				return nil, true // a field the reader did not provide is simply null
			}
			return nil, false
		}
	}

	rec := h.New(nil)
	rec.Values[c.Access.Owner] = p.ID
	if err := assignAll(rec, im.Fields, fileEnv(src.Fields), im.Fallback); err != nil {
		return nil, err
	}
	out := &ImportOutcome{}
	if im.Dedupe != "" && rec.Get(im.Dedupe) != nil {
		dup, err := h.First(ctx, Query{Where: []Cond{Eq(c.Access.Owner, p.ID), Eq(im.Dedupe, rec.Get(im.Dedupe))}})
		if err == nil {
			out.Duplicate = dup
			return out, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	for _, ln := range im.Links {
		target, err := h.findLink(ctx, p, rec, ln)
		if err != nil {
			return nil, err
		}
		if target != nil {
			rec.Values[ln.Via] = target.ID()
			out.Linked = append(out.Linked, target)
		}
	}
	if err := h.Save(ctx, rec); err != nil {
		return nil, err
	}
	out.Record = rec

	for _, ch := range im.Children {
		items := src.Children[ch.From]
		if len(items) < max(ch.Min, 1) {
			continue
		}
		rel := c.Relationship(ch.Relationship)
		th, err := r.Class(rel.Target)
		if err != nil {
			return nil, err
		}
		for i, item := range items {
			child := th.New(nil)
			child.Values[rel.Via] = rec.ID()
			if o := ownerSlotOf(th.Class); o != "" {
				child.Values[o] = p.ID
			}
			vals := map[string]any{"index": int64(i + 1)}
			for k, v := range item {
				vals[k] = v
			}
			if err := assignAll(child, ch.Fields, fileEnv(vals, layer{prefix: "self", rec: rec}), nil); err != nil {
				return nil, err
			}
			if err := th.Save(ctx, child); err != nil {
				return nil, fmt.Errorf("%s %d: %w", th.Class.Name, i+1, err)
			}
		}
	}

	for _, cr := range im.Creates {
		created, err := h.importCreate(ctx, p, rec, cr)
		if err != nil {
			return nil, err
		}
		out.Created = append(out.Created, created...)
	}
	// Reload: triggers and children may have changed linked rows.
	for i, l := range out.Linked {
		if lh, err := r.Class(l.Class.Name); err == nil {
			if fresh, err := lh.Load(ctx, l.ID()); err == nil {
				out.Linked[i] = fresh
			}
		}
	}
	return out, nil
}

func (h *Handle) findLink(ctx context.Context, p *Principal, rec *Record, ln model.ImportLink) (*Record, error) {
	rel := h.Class.BelongsToVia(ln.Via)
	th, err := h.reg.Class(rel.Target)
	if err != nil {
		return nil, err
	}
	selfEnv := envOf(layer{prefix: "self", rec: rec})
	var q Query
	for _, w := range ln.Where {
		v, err := w.Expr.Eval(selfEnv)
		if err != nil {
			return nil, fmt.Errorf("import link %s: %w", ln.Via, err)
		}
		if v == nil {
			return nil, nil // nothing to match on
		}
		q.Where = append(q.Where, Eq(w.Slot, exprToSlot(th.Class.Slot(w.Slot), v)))
	}
	if o := ownerSlotOf(th.Class); o != "" {
		q.Where = append(q.Where, Eq(o, p.ID))
	}
	q, ok := th.Scope(q, p)
	if !ok {
		return nil, nil
	}
	q.Limit = 100
	cands, err := th.List(ctx, q)
	if err != nil {
		return nil, err
	}
	var first *Record
	for _, cand := range cands {
		env := envOf(layer{prefix: "self", rec: rec}, layer{prefix: "item", rec: cand})
		if ln.If != nil {
			if v, err := ln.If.Eval(env); err != nil || !expr.Truthy(v) {
				continue
			}
		}
		if ln.Prefer == nil {
			return cand, nil
		}
		if v, err := ln.Prefer.Eval(env); err == nil && expr.Truthy(v) {
			return cand, nil
		}
		if first == nil {
			first = cand
		}
	}
	return first, nil
}

func (h *Handle) importCreate(ctx context.Context, p *Principal, rec *Record, cr model.ImportCreate) ([]*Record, error) {
	r := h.reg
	from, err := r.Class(cr.FromClass)
	if err != nil {
		return nil, err
	}
	target, err := r.Class(cr.Class)
	if err != nil {
		return nil, err
	}
	selfEnv := envOf(layer{prefix: "self", rec: rec})
	var q Query
	for _, w := range cr.FromWhere {
		v, err := w.Expr.Eval(selfEnv)
		if err != nil {
			return nil, fmt.Errorf("import create %s: %w", cr.Class, err)
		}
		q.Where = append(q.Where, Eq(w.Slot, exprToSlot(from.Class.Slot(w.Slot), v)))
	}
	q, ok := from.Scope(q, p)
	if !ok {
		return nil, nil
	}
	q.Limit = 500
	rows, err := from.List(ctx, q)
	if err != nil {
		return nil, err
	}
	var items []*Record
	seen := map[int64]bool{}
	for _, row := range rows {
		item := row
		if cr.FromFollow != "" {
			if item, err = from.Parent(ctx, row, cr.FromFollow); err != nil {
				continue
			}
			if readable, _ := r.Readable(ctx, item, p); !readable {
				continue
			}
		}
		if !seen[item.ID()] {
			seen[item.ID()] = true
			items = append(items, item)
		}
	}
	var out []*Record
	for _, item := range items {
		env := envOf(layer{prefix: "self", rec: rec}, layer{prefix: "item", rec: item})
		if cr.If != nil {
			v, err := cr.If.Eval(env)
			if err != nil {
				return nil, fmt.Errorf("import create %s: %w", cr.Class, err)
			}
			if !expr.Truthy(v) {
				continue
			}
		}
		n := target.New(nil)
		if o := ownerSlotOf(target.Class); o != "" {
			n.Values[o] = p.ID
		}
		if err := assignAll(n, cr.Set, env, nil); err != nil {
			return nil, err
		}
		if o := ownerSlotOf(target.Class); o != "" {
			n.Values[o] = p.ID // never let a mapping assign someone else as owner
		}
		if len(cr.Unique) > 0 {
			var uq Query
			for _, u := range cr.Unique {
				uq.Where = append(uq.Where, Eq(u, n.Get(u)))
			}
			if _, err := target.First(ctx, uq); err == nil {
				continue
			} else if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
		}
		if err := target.Save(ctx, n); err != nil {
			var fe FieldErrors
			if errors.As(err, &fe) {
				continue // the mapping produced an invalid row (e.g. a missing time): skip it
			}
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

// assignAll evaluates each mapping and stores it, coercing to the slot type,
// truncating over-long strings, and applying enum fallbacks. Null values are
// left unset so defaults apply.
func assignAll(rec *Record, as []model.Assign, env expr.Env, fallback map[string]string) error {
	for _, a := range as {
		s := rec.Class.Slot(a.Slot)
		if s == nil {
			continue
		}
		v, err := a.Expr.Eval(env)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", rec.Class.Name, a.Slot, err)
		}
		if v == nil {
			continue
		}
		v = exprToSlot(s, v)
		if str, ok := v.(string); ok {
			if s.EnumObj != nil && !s.EnumObj.HasValue(str) {
				fb, ok := fallback[a.Slot]
				if !ok {
					continue
				}
				v = fb
			}
			if s.Max != nil && (s.Type == "string" || s.Type == "text") && float64(len([]rune(str))) > *s.Max {
				v = string([]rune(str)[:int(*s.Max)])
			}
		}
		cv, err := Coerce(s, v)
		if err != nil {
			continue // an unusable value is skipped rather than failing the whole import
		}
		rec.Values[a.Slot] = cv
	}
	return nil
}

func ownerSlotOf(c *model.Class) string {
	if c.Access == nil {
		return ""
	}
	return c.Access.Owner
}

func containsDot(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return true
		}
	}
	return false
}
