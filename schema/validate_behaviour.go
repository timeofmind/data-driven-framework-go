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
	errs = append(errs, validateLibrary(c, m)...)
	errs = append(errs, validateRules(c, m)...)
	return errs
}

// validateRules checks title, slot when/required_when/options, belongs_to
// match and class checks. Rules that run in the browser (title, when,
// required_when) may read only the record's own slots; the rest may also
// follow belongs_to links (set_id.workout_id.sport, or set.workout.sport).
func validateRules(c *model.Class, m *model.Model) []string {
	var errs []string
	add := func(where string, problems []string) {
		for _, p := range problems {
			errs = append(errs, fmt.Sprintf("Class '%s', %s: %s", c.Name, where, p))
		}
	}
	own := scope{"": c}
	add("title", checkExpr(c.Title, own))
	for _, s := range c.Slots {
		add("slot "+s.Name+" when", checkExpr(s.When, own))
		add("slot "+s.Name+" required_when", checkExpr(s.RequiredWhen, own))
		if len(s.Options) > 0 && s.Type != "enum" {
			add("slot "+s.Name, []string{"options apply to enum slots only"})
		}
		for _, o := range s.Options {
			add("slot "+s.Name+" options", checkPathExpr(o.When, c, m))
			if len(o.Values) == 0 {
				add("slot "+s.Name+" options", []string{"each rule needs values"})
			}
			if e := m.Enum(s.Enum); e != nil {
				for _, v := range o.Values {
					if !e.HasValue(v) {
						add("slot "+s.Name+" options", []string{fmt.Sprintf("%q is not a value of %s", v, e.Name)})
					}
				}
			}
		}
	}
	for _, r := range c.Relationships {
		if len(r.Match) == 0 {
			continue
		}
		if !r.IsBelongsTo() {
			add("relationship "+r.Name, []string{"match applies to belongs_to only"})
			continue
		}
		t := m.Class(r.Target)
		for _, a := range r.Match {
			if t != nil && !t.HasSlot(a.Slot) {
				add("relationship "+r.Name+" match", []string{fmt.Sprintf("%s has no slot %q", r.Target, a.Slot)})
			}
			add("relationship "+r.Name+" match", checkPathExpr(a.Expr, c, m))
		}
	}
	for _, ck := range c.Checks {
		if ck.Client {
			add("check (client)", checkExpr(ck.Expr, own))
		} else {
			add("check", checkPathExpr(ck.Expr, c, m))
		}
		if ck.Slot != "" && !c.HasSlot(ck.Slot) {
			add("check", []string{fmt.Sprintf("slot %q is not defined", ck.Slot)})
		}
	}
	return errs
}

// checkPathExpr accepts own slots and dotted paths through belongs_to links.
func checkPathExpr(e *expr.Expr, c *model.Class, m *model.Model) []string {
	if e == nil {
		return nil
	}
	var errs []string
	for _, id := range e.Idents() {
		if _, err := model.ResolvePath(m, c, id); err != nil {
			errs = append(errs, fmt.Sprintf("expression %q: %v", e, err))
		}
	}
	return errs
}

// validateLibrary checks filters, actions and usage.
func validateLibrary(c *model.Class, m *model.Model) []string {
	var errs []string
	add := func(where, format string, a ...any) {
		errs = append(errs, fmt.Sprintf("Class '%s', %s: %s", c.Name, where, fmt.Sprintf(format, a...)))
	}
	names := map[string]bool{}
	for _, f := range c.Filters {
		where := "filter " + f.Name
		if f.Name == "" || names[f.Name] || c.HasSlot(f.Name) || f.Name == "q" || f.Name == "page" || f.Name == "order" || f.Name == "mine" {
			add(where, "name must be unique and not a slot or reserved parameter (q, page, order, mine)")
		}
		names[f.Name] = true
		if f.Match != "all" && f.Match != "any" {
			add(where, "match must be all or any")
		}
		rel := c.RelationshipByName(f.Via)
		if rel == nil || !rel.IsHasMany() {
			add(where, "via %q is not a has_many relationship", f.Via)
			continue
		}
		val := m.FilterValueClass(c, f)
		if val == nil {
			add(where, "slot %q is not a belongs_to FK on %s", f.Slot, rel.Target)
			continue
		}
		if f.IncludeChildren != "" {
			if br := val.BelongsToVia(f.IncludeChildren); br == nil || br.Target != val.Name {
				add(where, "include_children %q is not a belongs_to FK from %s to itself", f.IncludeChildren, val.Name)
			}
		}
		if f.GroupBy != "" && val.BelongsToVia(f.GroupBy) == nil {
			add(where, "group_by %q is not a belongs_to FK on %s", f.GroupBy, val.Name)
		}
		if f.CollapseGroups && f.GroupBy == "" {
			add(where, "collapse_groups needs group_by")
		}
		if f.RankByUsage && (c.Usage == nil || c.Usage.Filter != f.Name) {
			add(where, "rank_by_usage needs a usage block recording this filter (usage.filter: %s)", f.Name)
		}
	}
	reserved := map[string]bool{"new": true, "edit": true, "delete": true}
	seen := map[string]bool{}
	for _, a := range c.Actions {
		where := "action " + a.Name
		if reserved[a.Name] || seen[a.Name] {
			add(where, "name must be unique and not new, edit or delete")
		}
		seen[a.Name] = true
		if len(a.Copy) == 0 && len(a.Set) == 0 {
			add(where, "nothing to do: give copy and/or set")
		}
		for _, path := range a.Copy {
			cur := c
			for _, seg := range strings.Split(path, ".") {
				rel := cur.RelationshipByName(seg)
				if rel == nil || !rel.IsHasMany() || m.Class(rel.Target) == nil {
					add(where, "copy path %q: %q is not a has_many of %s", path, seg, cur.Name)
					break
				}
				cur = m.Class(rel.Target)
			}
		}
		sc := scope{"": c, "source": c}
		for _, as := range a.Set {
			if !c.HasSlot(as.Slot) {
				add(where, "%s has no slot %q to set", c.Name, as.Slot)
			}
			for _, p := range checkExpr(as.Expr, scope{"source": c}) {
				add(where, "%s", p)
			}
		}
		for _, p := range checkExpr(a.When, sc) {
			add(where, "%s", p)
		}
	}
	if u := c.Usage; u != nil {
		where := "usage"
		log := m.Class(u.Log)
		if log == nil {
			add(where, "log class %q is not defined", u.Log)
			return errs
		}
		if log.IsJoinedChild() {
			add(where, "log class %s must not use is_a", log.Name)
		}
		if br := log.BelongsToVia(u.Via); br == nil || br.Target != c.Name {
			add(where, "via %q is not a belongs_to FK from %s to %s", u.Via, log.Name, c.Name)
		}
		kind := log.Slot(u.Kind)
		if kind == nil || kind.Type != "enum" {
			add(where, "kind %q is not an enum slot on %s", u.Kind, log.Name)
		}
		if u.Filter != "" {
			f := c.FilterByName(u.Filter)
			if f == nil {
				add(where, "filter %q is not one of %s's filters", u.Filter, c.Name)
			} else if val := m.FilterValueClass(c, f); val != nil {
				rel := log.RelationshipByName(u.Values)
				var vc *model.Class
				if rel != nil && rel.IsHasMany() {
					vc = m.Class(rel.Target)
				}
				if vc == nil || vc.BelongsToTarget(val.Name) == nil {
					add(where, "values %q must be a has_many of %s whose class has a belongs_to to %s", u.Values, log.Name, val.Name)
				}
			}
		}
		if len(u.Uses) == 0 {
			add(where, "uses is empty")
		}
		for _, use := range u.Uses {
			w := where + " use " + use.Kind
			if kind != nil && kind.EnumObj != nil && !kind.EnumObj.HasValue(use.Kind) {
				add(w, "kind %q is not a value of %s", use.Kind, kind.Enum)
			}
			switch {
			case use.LinkClass != "" && use.Action != "":
				add(w, "give link or action, not both")
			case use.LinkClass != "":
				lc := m.Class(use.LinkClass)
				if lc == nil {
					add(w, "link class %q is not defined", use.LinkClass)
				} else if br := lc.BelongsToVia(use.LinkVia); br == nil || br.Target != c.Name {
					add(w, "link %s.%s is not a belongs_to FK to %s", use.LinkClass, use.LinkVia, c.Name)
				}
			case use.Action != "":
				if c.ActionByName(use.Action) == nil {
					add(w, "action %q is not one of %s's actions", use.Action, c.Name)
				}
			default:
				add(w, "needs link or action")
			}
			if use.Record != "" && log.BelongsToVia(use.Record) == nil {
				add(w, "record %q is not a belongs_to FK on %s", use.Record, log.Name)
			}
		}
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
