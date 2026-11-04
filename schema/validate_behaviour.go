package schema

import (
	"fmt"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// scope maps an identifier prefix ("" for bare names, "self", "item", …) to
// the class whose slots it may name. A nil class accepts any name under that
// prefix (e.g. bare names in import mappings are fields of the parsed file).
type scope map[string]*model.Class

func checkExpr(e *expr.Expr, sc scope) []string {
	if e == nil {
		return nil
	}
	var errs []string
	for _, id := range e.Idents() {
		prefix, name := "", id
		if i := strings.IndexByte(id, '.'); i >= 0 {
			prefix, name = id[:i], id[i+1:]
		}
		c, ok := sc[prefix]
		if !ok {
			if prefix == "" {
				errs = append(errs, fmt.Sprintf("expression %q: unknown name %q", e, id))
			} else {
				errs = append(errs, fmt.Sprintf("expression %q: unknown prefix %q in %q", e, prefix, id))
			}
			continue
		}
		if c != nil && !c.HasSlot(name) && !(name == "id" && c.PKSlot() != nil) {
			errs = append(errs, fmt.Sprintf("expression %q: %s has no slot %q", e, c.Name, name))
		}
	}
	return errs
}

func validateBehaviour(c *model.Class, m *model.Model) []string {
	var errs []string
	add := func(where string, problems []string) {
		for _, p := range problems {
			errs = append(errs, fmt.Sprintf("Class '%s', %s: %s", c.Name, where, p))
		}
	}
	own := scope{"": c}
	add("calendar_label", checkExpr(c.CalendarLabel, own))
	for _, cp := range c.Computed {
		add("computed "+cp.Name, checkExpr(cp.Expr, own))
		add("computed "+cp.Name, checkExpr(cp.When, own))
		switch cp.Format {
		case "", "duration", "number", "text":
		default:
			add("computed "+cp.Name, []string{"format must be duration, number or text"})
		}
	}
	for _, tr := range c.Triggers {
		rel := c.BelongsToVia(tr.Via)
		if rel == nil {
			add("trigger", []string{fmt.Sprintf("via %q is not a belongs_to FK", tr.Via)})
			continue
		}
		target := m.Class(rel.Target)
		sc := scope{"self": c, "target": target}
		add("trigger via "+tr.Via, checkExpr(tr.If, sc))
		for _, a := range tr.Set {
			if target != nil && !target.HasSlot(a.Slot) {
				add("trigger via "+tr.Via, []string{fmt.Sprintf("%s has no slot %q to set", target.Name, a.Slot)})
			}
			add("trigger via "+tr.Via, checkExpr(a.Expr, sc))
		}
	}
	for _, r := range c.Relationships {
		if r.Rank != nil && r.Rank.BestPer != "" {
			if t := m.Class(r.Target); t != nil && !t.HasSlot(r.Rank.BestPer) {
				add("relationship "+r.Name, []string{fmt.Sprintf("rank.best_per slot %q is not on %s", r.Rank.BestPer, r.Target)})
			}
		}
		if (r.Rank != nil || r.AddLabel != "" || r.AddWhen != nil) && !r.IsHasMany() {
			add("relationship "+r.Name, []string{"rank, add_label and add_when apply to has_many only"})
		}
		add("relationship "+r.Name+" add_when", checkExpr(r.AddWhen, own))
	}
	if im := c.Import; im != nil {
		errs = append(errs, validateImport(c, m, im)...)
	}
	return errs
}

func validateImport(c *model.Class, m *model.Model, im *model.Import) []string {
	var errs []string
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Sprintf("Class '%s', import: ", c.Name)+fmt.Sprintf(format, a...))
	}
	var auth *model.Class
	for _, k := range m.Classes() {
		if k.Auth != nil {
			auth = k
		}
	}
	if c.Access == nil || c.Access.Owner == "" {
		add("the class needs an access owner slot (imports belong to the uploader)")
	}
	if len(im.Formats) == 0 {
		add("formats is required")
	}
	if im.Dedupe != "" && !c.HasSlot(im.Dedupe) {
		add("dedupe slot %q is not defined", im.Dedupe)
	}
	// Bare names are fields of the parsed file; owner.* is the uploader.
	file := scope{"": nil, "owner": auth}
	for _, p := range checkExpr(im.Timezone, scope{"owner": auth}) {
		add("timezone: %s", p)
	}
	for _, a := range im.Fields {
		if !c.HasSlot(a.Slot) {
			add("fields: no slot %q", a.Slot)
		}
		for _, p := range checkExpr(a.Expr, file) {
			add("fields.%s: %s", a.Slot, p)
		}
	}
	for slot, v := range im.Fallback {
		s := c.Slot(slot)
		if s == nil || s.EnumObj == nil || !s.EnumObj.HasValue(v) {
			add("fallback %s: %q is not a value of that enum slot", slot, v)
		}
	}
	for _, ch := range im.Children {
		r := c.Relationship(ch.Relationship)
		if r == nil || !r.IsHasMany() {
			add("children: %q is not a has_many relationship", ch.Relationship)
			continue
		}
		t := m.Class(r.Target)
		for _, a := range ch.Fields {
			if t != nil && !t.HasSlot(a.Slot) {
				add("children %s: %s has no slot %q", ch.Relationship, r.Target, a.Slot)
			}
			for _, p := range checkExpr(a.Expr, scope{"": nil, "self": c, "owner": auth}) {
				add("children %s.%s: %s", ch.Relationship, a.Slot, p)
			}
		}
	}
	for _, ln := range im.Links {
		r := c.BelongsToVia(ln.Via)
		if r == nil {
			add("link: %q is not a belongs_to FK", ln.Via)
			continue
		}
		t := m.Class(r.Target)
		sc := scope{"self": c, "item": t, "owner": auth}
		for _, a := range ln.Where {
			if t != nil && !t.HasSlot(a.Slot) {
				add("link %s where: %s has no slot %q", ln.Via, r.Target, a.Slot)
			}
			for _, p := range checkExpr(a.Expr, sc) {
				add("link %s where.%s: %s", ln.Via, a.Slot, p)
			}
		}
		for _, p := range append(checkExpr(ln.If, sc), checkExpr(ln.Prefer, sc)...) {
			add("link %s: %s", ln.Via, p)
		}
	}
	for _, cr := range im.Creates {
		target := m.Class(cr.Class)
		from := m.Class(cr.FromClass)
		if target == nil || from == nil {
			add("create: class %q or from.class %q is not defined", cr.Class, cr.FromClass)
			continue
		}
		item := from
		if cr.FromFollow != "" {
			r := from.BelongsToVia(cr.FromFollow)
			if r == nil {
				add("create %s: from.follow %q is not a belongs_to FK of %s", cr.Class, cr.FromFollow, from.Name)
				continue
			}
			item = m.Class(r.Target)
		}
		sc := scope{"self": c, "item": item, "owner": auth}
		for _, a := range cr.FromWhere {
			if !from.HasSlot(a.Slot) {
				add("create %s from.where: %s has no slot %q", cr.Class, from.Name, a.Slot)
			}
			for _, p := range checkExpr(a.Expr, sc) {
				add("create %s from.where.%s: %s", cr.Class, a.Slot, p)
			}
		}
		for _, p := range checkExpr(cr.If, sc) {
			add("create %s if: %s", cr.Class, p)
		}
		for _, u := range cr.Unique {
			if !target.HasSlot(u) {
				add("create %s unique: no slot %q", cr.Class, u)
			}
		}
		for _, a := range cr.Set {
			if !target.HasSlot(a.Slot) {
				add("create %s set: no slot %q", cr.Class, a.Slot)
			}
			for _, p := range checkExpr(a.Expr, sc) {
				add("create %s set.%s: %s", cr.Class, a.Slot, p)
			}
		}
	}
	return errs
}
