package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// Filters, usage ranking and copy actions: the pieces a shared library of
// records needs (find by related values, rank by how often others chose a
// record under the same values, take a copy of your own).

// maxFilterValues bounds how many values one filter accepts in a request.
const maxFilterValues = 20

// FilterConds returns conditions limiting h's class to records related to the
// given values of filter f (through its link class). With match "all" every
// value must be present; with "any", one is enough. With include_children a
// value also matches any of its descendants.
func (h *Handle) FilterConds(ctx context.Context, f *model.Filter, ids []int64) ([]Cond, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > maxFilterValues {
		ids = ids[:maxFilterValues]
	}
	rel := h.Class.RelationshipByName(f.Via)
	if rel == nil {
		return nil, fmt.Errorf("%s: filter %s: no relationship %q", h.Class.Name, f.Name, f.Via)
	}
	link, err := h.reg.Class(rel.Target)
	if err != nil {
		return nil, err
	}
	val := h.reg.Model.FilterValueClass(h.Class, f)
	pk, err := h.pk()
	if err != nil {
		return nil, err
	}
	groups := [][]int64{}
	for _, id := range ids {
		set := []int64{id}
		if f.IncludeChildren != "" && val != nil {
			if set, err = h.reg.Descendants(ctx, val, f.IncludeChildren, id); err != nil {
				return nil, err
			}
		}
		groups = append(groups, set)
	}
	if f.Match == "any" {
		var all []int64
		for _, g := range groups {
			all = append(all, g...)
		}
		groups = [][]int64{all}
	}
	var conds []Cond
	for i, g := range groups {
		vals := make([]any, len(g))
		for j, v := range g {
			vals[j] = v
		}
		sq, err := link.columnSubquery(rel.Via, Query{Where: []Cond{{Field: f.Slot, Op: "in", Value: vals}}}, fmt.Sprintf("f%d_", i))
		if err != nil {
			return nil, err
		}
		conds = append(conds, Cond{Field: pk.Name, Op: "insub", Value: sq})
	}
	return conds, nil
}

// columnSubquery renders "SELECT <col> FROM … WHERE …" over h's class.
func (h *Handle) columnSubquery(col string, qr Query, alias string) (subquery, error) {
	refs, _ := h.columnRefsAs(alias)
	ref, ok := refs[col]
	if !ok {
		return subquery{}, fmt.Errorf("%s has no field %q", h.Class.Name, col)
	}
	from, err := h.fromClauseAs(alias)
	if err != nil {
		return subquery{}, err
	}
	where, args, err := h.buildWhere(qr, refs)
	if err != nil {
		return subquery{}, err
	}
	return subquery{sql: "SELECT " + ref + " FROM " + from + where, args: args}, nil
}

// Descendants returns id and every row below it through the self-referencing
// FK slot of class c (e.g. a tag and its narrower tags), to a depth of 8.
func (r *Registry) Descendants(ctx context.Context, c *model.Class, fk string, id int64) ([]int64, error) {
	h, err := r.Class(c.Name)
	if err != nil {
		return nil, err
	}
	pk, err := h.pk()
	if err != nil {
		return nil, err
	}
	out := []int64{id}
	seen := map[int64]bool{id: true}
	frontier := []any{id}
	for depth := 0; depth < 8 && len(frontier) > 0; depth++ {
		recs, err := h.List(ctx, Query{Where: []Cond{{Field: fk, Op: "in", Value: frontier}}})
		if err != nil {
			return nil, err
		}
		frontier = nil
		for _, rec := range recs {
			if n := rec.Int(pk.Name); !seen[n] {
				seen[n] = true
				out = append(out, n)
				frontier = append(frontier, n)
			}
		}
	}
	return out, nil
}

// RankByUsage orders qr (before its own ordering) by the class's usage log:
// first by how many people chose each record with exactly these filter
// values, then with at least these values, then by how many chose it at all.
// With no values only the overall count applies.
func (h *Handle) RankByUsage(qr Query, ids []int64) (Query, error) {
	u := h.Class.Usage
	if u == nil {
		return qr, fmt.Errorf("%s has no usage block", h.Class.Name)
	}
	if len(ids) > maxFilterValues {
		ids = ids[:maxFilterValues]
	}
	log, err := h.reg.Class(u.Log)
	if err != nil {
		return qr, err
	}
	q := h.reg.Dialect.Quote
	lpk, err := log.pk()
	if err != nil {
		return qr, err
	}
	who := "*"
	if o := ownerSlot(log.Class); o != "" {
		who = "DISTINCT u." + q(o)
	}
	head := fmt.Sprintf("(SELECT COUNT(%s) FROM %s u WHERE u.%s = {id}", who, q(log.Class.EffectiveTable()), q(u.Via))
	var terms []subquery
	if len(ids) > 0 && u.Filter != "" {
		f := h.Class.FilterByName(u.Filter)
		val := h.reg.Model.FilterValueClass(h.Class, f)
		vrel := log.Class.RelationshipByName(u.Values)
		vc := h.reg.Model.Class(vrel.Target)
		vslot := vc.BelongsToTarget(val.Name).Via
		vt := q(vc.EffectiveTable())
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		has := fmt.Sprintf("(SELECT COUNT(*) FROM %s v WHERE v.%s = u.%s AND v.%s IN (%s)) = %d",
			vt, q(vrel.Via), q(lpk.Name), q(vslot), ph, len(ids))
		only := fmt.Sprintf("(SELECT COUNT(*) FROM %s w WHERE w.%s = u.%s) = %d", vt, q(vrel.Via), q(lpk.Name), len(ids))
		terms = append(terms,
			subquery{sql: head + " AND " + has + " AND " + only + ")", args: args},
			subquery{sql: head + " AND " + has + ")", args: args})
	}
	terms = append(terms, subquery{sql: head + ")"})
	qr.rank = terms
	return qr, nil
}

// LogUsage records that p used the record usedID of class used in the way
// use describes: newID is the record the use created (the scheduled row, the
// copy), values the filter values p had chosen when finding it. Values that
// are not readable rows of the filter's value class are dropped.
func (r *Registry) LogUsage(ctx context.Context, p *Principal, used *model.Class, usedID int64, use *model.UsageUse, newID int64, values []int64) error {
	u := used.Usage
	if u == nil || p == nil {
		return nil
	}
	log, err := r.Class(u.Log)
	if err != nil {
		return err
	}
	row := log.New(map[string]any{u.Via: usedID, u.Kind: use.Kind})
	if o := ownerSlot(log.Class); o != "" {
		row.Values[o] = p.ID
	}
	if use.Record != "" && newID != 0 {
		row.Values[use.Record] = newID
	}
	if err := log.Save(ctx, row); err != nil {
		return err
	}
	if u.Filter == "" || len(values) == 0 {
		return nil
	}
	f := used.FilterByName(u.Filter)
	val := r.Model.FilterValueClass(used, f)
	vh, err := r.Class(val.Name)
	if err != nil {
		return err
	}
	vrel := log.Class.RelationshipByName(u.Values)
	vc, err := r.Class(vrel.Target)
	if err != nil {
		return err
	}
	vslot := vc.Class.BelongsToTarget(val.Name).Via
	seen := map[int64]bool{}
	for i, id := range values {
		if i >= maxFilterValues || seen[id] {
			continue
		}
		seen[id] = true
		rec, err := vh.Load(ctx, id)
		if err != nil {
			continue
		}
		if ok, _ := r.Readable(ctx, rec, p); !ok {
			continue
		}
		if err := vc.Save(ctx, vc.New(map[string]any{vrel.Via: row.ID(), vslot: id})); err != nil {
			return err
		}
	}
	return nil
}

// Copy runs a copy action on src for p: a new record owned by p with src's
// values and the action's set applied (source.* is src), then the action's
// has_many paths copied beneath it with set_children applied (source.* the
// child copied, root.* src).
// check, when given, vets every record before it is saved (e.g. that its
// links are allowed); on any failure the partial copy is deleted.
func (r *Registry) Copy(ctx context.Context, p *Principal, src *Record, act *model.Action, check func(*Record) error) (*Record, error) {
	h, err := r.Class(src.Class.Name)
	if err != nil {
		return nil, err
	}
	dst := copyValues(h, src, p)
	env := envOf(layer{prefix: "source", rec: src})
	for _, as := range act.Set {
		v, err := as.Expr.Eval(env)
		if err != nil {
			return nil, fmt.Errorf("action %s: %s: %w", act.Name, as.Slot, err)
		}
		dst.Values[as.Slot] = exprToSlot(src.Class.Slot(as.Slot), v)
	}
	if check != nil {
		if err := check(dst); err != nil {
			return nil, err
		}
	}
	if err := h.Save(ctx, dst); err != nil {
		return nil, err
	}
	cc := copyCtx{p: p, root: src, act: act, check: check, linked: map[string]int64{}}
	if err := r.copyChildren(ctx, cc, src, dst, pathTree(act.Copy), ""); err != nil {
		_ = h.Delete(ctx, dst.ID())
		for key, id := range cc.linked { // and the linked records copied so far
			if lh, herr := r.Class(key[:strings.IndexByte(key, ':')]); herr == nil {
				_ = lh.Delete(ctx, id)
			}
		}
		return nil, err
	}
	return dst, nil
}

type copyCtx struct {
	p      *Principal
	root   *Record
	act    *model.Action
	check  func(*Record) error
	linked map[string]int64 // "Class:id" → id of its copy (copy_links)
}

type paths map[string]paths

func pathTree(list []string) paths {
	t := paths{}
	for _, p := range list {
		cur := t
		for _, seg := range strings.Split(p, ".") {
			if cur[seg] == nil {
				cur[seg] = paths{}
			}
			cur = cur[seg]
		}
	}
	return t
}

func (r *Registry) copyChildren(ctx context.Context, cc copyCtx, src, dst *Record, tree paths, prefix string) error {
	names := make([]string, 0, len(tree))
	for n := range tree {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		rel := src.Class.RelationshipByName(name)
		ch, err := r.Class(rel.Target)
		if err != nil {
			return err
		}
		kids, err := r.MustClass(src.Class.Name).Children(ctx, src, name, Query{})
		if err != nil {
			return err
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		for _, kid := range kids {
			c := copyValues(ch, kid, cc.p)
			c.Values[rel.Via] = dst.ID()
			if sets := cc.act.SetChildren[path]; len(sets) > 0 {
				env := envOf(layer{prefix: "source", rec: kid}, layer{prefix: "root", rec: cc.root})
				for _, as := range sets {
					v, err := as.Expr.Eval(env)
					if err != nil {
						return fmt.Errorf("action %s: %s.%s: %w", cc.act.Name, path, as.Slot, err)
					}
					c.Values[as.Slot] = exprToSlot(ch.Class.Slot(as.Slot), v)
				}
			}
			if err := r.copyLinked(ctx, cc, c, path); err != nil {
				return err
			}
			if cc.check != nil {
				if err := cc.check(c); err != nil {
					return err
				}
			}
			if err := ch.Save(ctx, c); err != nil {
				return err
			}
			if err := r.copyChildren(ctx, cc, kid, c, tree[name], path); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyValues is an unsaved record with src's stored values, minus keys,
// generated values and passwords, owned by p when the class has an owner.
func copyValues(h *Handle, src *Record, p *Principal) *Record {
	vals := map[string]any{}
	for _, s := range src.Class.AllSlots() {
		if s.Autogenerated || s.Type == "password" || s == src.Class.PKSlot() {
			continue
		}
		vals[s.Name] = src.Values[s.Name]
	}
	if o := ownerSlot(src.Class); o != "" && p != nil {
		vals[o] = p.ID
	}
	return h.New(vals)
}

// ActionAllowed reports whether act's when-condition holds for rec.
func ActionAllowed(act *model.Action, rec *Record) bool {
	if act.When == nil {
		return true
	}
	v, err := act.When.Eval(envOf(layer{rec: rec}, layer{prefix: "source", rec: rec}))
	return err == nil && expr.Truthy(v)
}

// copyLinked copies the records c links to that the action's copy_links name
// for this path, once each, and points c at the copies.
func (r *Registry) copyLinked(ctx context.Context, cc copyCtx, c *Record, path string) error {
	for _, fk := range sortedKeysOf(cc.act.CopyLinks) {
		if !strings.HasPrefix(fk, path+".") || strings.Contains(fk[len(path)+1:], ".") {
			continue
		}
		slot := fk[len(path)+1:]
		id := c.Int(slot)
		rel := c.Class.BelongsToVia(slot)
		if id == 0 || rel == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d", rel.Target, id)
		if done, ok := cc.linked[key]; ok {
			c.Values[slot] = done
			continue
		}
		th, err := r.Class(rel.Target)
		if err != nil {
			return err
		}
		target, err := th.Load(ctx, id)
		if err != nil {
			c.Values[slot] = nil // gone: nothing to copy
			continue
		}
		cp, err := r.Copy(ctx, cc.p, target, th.Class.ActionByName(cc.act.CopyLinks[fk]), cc.check)
		if err != nil {
			return fmt.Errorf("copy %s %d: %w", rel.Target, id, err)
		}
		cc.linked[key] = cp.ID()
		c.Values[slot] = cp.ID()
	}
	return nil
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
