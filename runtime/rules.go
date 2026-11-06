package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// Rules declared in the schema (v0.5): title, slot when / required_when /
// options, belongs_to match and class checks. Save enforces all of them; the
// helpers here also let front ends show only what applies.

// ownEnv evaluates against the record's own slots, plus the link paths
// LoadLinks has filled in.
func ownEnv(rec *Record) expr.Env {
	own := envOf(layer{rec: rec})
	return func(name string) (any, bool) {
		if strings.Contains(name, ".") {
			v, ok := rec.links[name]
			return v, ok
		}
		return own(name)
	}
}

// LoadLinks fetches the linked values (set.workout.sport) the class's when
// and required_when rules read, so SlotApplies and SlotRequired can use them.
// Save calls it; front ends call it before deciding which fields to show.
func (r *Registry) LoadLinks(ctx context.Context, rec *Record) {
	rec.links = map[string]any{}
	for _, s := range rec.Class.AllSlots() {
		for _, e := range []*expr.Expr{s.When, s.RequiredWhen} {
			if e == nil {
				continue
			}
			for _, id := range e.Idents() {
				if _, done := rec.links[id]; done || !strings.Contains(id, ".") {
					continue
				}
				if v, err := r.pathValue(ctx, rec, id); err == nil {
					rec.links[id] = v
				}
			}
		}
	}
}

// SlotApplies reports whether a slot's when-condition holds for rec. A rule
// reading links that were not loaded counts as holding, so nothing is hidden
// or cleared on a guess.
func SlotApplies(rec *Record, s *model.Slot) bool {
	if s.When == nil {
		return true
	}
	v, err := s.When.Eval(ownEnv(rec))
	if err != nil {
		return true
	}
	return expr.Truthy(v)
}

// SlotRequired reports whether s must have a value on rec.
func SlotRequired(rec *Record, s *model.Slot) bool {
	if s.Required {
		return true
	}
	if s.RequiredWhen == nil || !SlotApplies(rec, s) {
		return false
	}
	v, err := s.RequiredWhen.Eval(ownEnv(rec))
	return err == nil && expr.Truthy(v)
}

// clearInapplicable empties slots whose when-condition does not hold, so a
// value entered for one choice is not kept after switching to another.
func clearInapplicable(rec *Record) {
	for _, s := range rec.Class.AllSlots() {
		if s.When != nil && !SlotApplies(rec, s) {
			rec.Values[s.Name] = nil
		}
	}
}

// pathEnv evaluates against own slots plus dotted paths through belongs_to
// links (set_id.workout_id.sport), loading the linked records once each.
func (r *Registry) pathEnv(ctx context.Context, rec *Record) expr.Env {
	own := ownEnv(rec)
	cache := map[string]any{}
	return func(name string) (any, bool) {
		if !strings.Contains(name, ".") {
			return own(name)
		}
		if v, ok := cache[name]; ok {
			return v, true
		}
		v, err := r.pathValue(ctx, rec, name)
		if err != nil {
			return nil, false
		}
		cache[name] = v
		return v, true
	}
}

func (r *Registry) pathValue(ctx context.Context, rec *Record, path string) (any, error) {
	steps, err := model.ResolvePath(r.Model, rec.Class, path)
	if err != nil {
		return nil, err
	}
	cur := rec
	for _, st := range steps {
		id := cur.Int(st.Rel.Via)
		if id == 0 {
			return nil, nil // link not set: the value is null
		}
		h, err := r.Class(st.Class.Name)
		if err != nil {
			return nil, err
		}
		if cur, err = h.Load(ctx, id); err != nil {
			return nil, nil
		}
	}
	v, _ := ownEnv(cur)(path[strings.LastIndexByte(path, '.')+1:])
	return v, nil
}

// AllowedValues returns the enum values an options rule allows for s on rec,
// or nil when no rule applies (every value is allowed).
func (r *Registry) AllowedValues(ctx context.Context, rec *Record, s *model.Slot) []string {
	env := r.pathEnv(ctx, rec)
	for _, o := range s.Options {
		if v, err := o.When.Eval(env); err == nil && expr.Truthy(v) {
			return o.Values
		}
	}
	return nil
}

// MatchConds returns the conditions a belongs_to's match puts on the target
// rows rec may link to. A match expression with no value yet (e.g. sport not
// chosen) adds no condition.
func (r *Registry) MatchConds(ctx context.Context, rec *Record, rel *model.Relationship) []Cond {
	env := r.pathEnv(ctx, rec)
	var out []Cond
	for _, a := range rel.Match {
		v, err := a.Expr.Eval(env)
		if err != nil || v == nil {
			continue
		}
		out = append(out, Eq(a.Slot, matchValue(v)))
	}
	return out
}

// matchValue turns an expression value back into a column value.
func matchValue(v any) any {
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}

// CheckRules enforces options, match and class checks; Save calls it after
// the slot rules in Validate.
func (r *Registry) CheckRules(ctx context.Context, rec *Record) FieldErrors {
	errs := FieldErrors{}
	c := rec.Class
	for _, s := range c.AllSlots() {
		if len(s.Options) == 0 {
			continue
		}
		v := rec.String(s.Name)
		if v == "" {
			continue
		}
		if allowed := r.AllowedValues(ctx, rec, s); allowed != nil && !contains(allowed, v) {
			labels := make([]string, len(allowed))
			for i, a := range allowed {
				labels[i] = s.EnumObj.Label(a)
			}
			errs[s.Name] = "must be one of " + strings.Join(labels, ", ")
		}
	}
	for _, rel := range c.AllRelationships() {
		if len(rel.Match) == 0 || rec.Int(rel.Via) == 0 {
			continue
		}
		h, err := r.Class(rel.Target)
		if err != nil {
			continue
		}
		target, err := h.Load(ctx, rec.Int(rel.Via))
		if err != nil {
			continue // a missing target is reported elsewhere
		}
		for _, cd := range r.MatchConds(ctx, rec, rel) {
			if !Matches(target, []Cond{cd}) {
				errs[rel.Via] = "doesn't fit the rest of the form"
				if s := matchSource(rel, cd.Field, c); s != nil {
					errs[rel.Via] = "doesn't fit the chosen " + strings.ToLower(s.EffectiveLabel())
				}
				break
			}
		}
	}
	env := r.pathEnv(ctx, rec)
	for _, ck := range c.Checks {
		v, err := ck.Expr.Eval(env)
		if err == nil && expr.Truthy(v) {
			continue
		}
		if _, taken := errs[ck.Slot]; !taken {
			errs[ck.Slot] = ck.Message
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// matchSource is the own slot a match expression reads, when it is a bare name.
func matchSource(rel *model.Relationship, field string, c *model.Class) *model.Slot {
	for _, a := range rel.Match {
		if a.Slot == field {
			if ids := a.Expr.Idents(); len(ids) == 1 {
				return c.Slot(ids[0])
			}
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// GivenValues evaluates names (own slots or dotted link paths) for rec, for a
// browser form that has no input for them (generator.ClientClass.Given).
func (r *Registry) GivenValues(ctx context.Context, rec *Record, names []string) map[string]any {
	env := r.pathEnv(ctx, rec)
	out := map[string]any{}
	for _, n := range names {
		if v, ok := env(n); ok {
			out[n] = v
		}
	}
	return out
}

// Matches reports whether a target row satisfies conditions from MatchConds.
func Matches(target *Record, conds []Cond) bool {
	for _, cd := range conds {
		if fmt.Sprint(expr.Normalize(target.Values[cd.Field])) != fmt.Sprint(expr.Normalize(cd.Value)) {
			return false
		}
	}
	return true
}

// SequenceTaken returns, for each parent id, the numbers sequence slot s
// already uses among that parent's rows (ascending), leaving out row exclude.
func (r *Registry) SequenceTaken(ctx context.Context, c *model.Class, s *model.Slot, parents []int64, exclude int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if s.Sequence == "" || len(parents) == 0 {
		return out, nil
	}
	h, err := r.Class(c.Name)
	if err != nil {
		return nil, err
	}
	ids := make([]any, len(parents))
	for i, p := range parents {
		ids[i] = p
		out[p] = []int64{}
	}
	rows, err := h.List(ctx, Query{Where: []Cond{{Field: s.Sequence, Op: "in", Value: ids}}, OrderBy: s.Name + " asc"})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.ID() == exclude || row.Values[s.Name] == nil {
			continue
		}
		p := row.Int(s.Sequence)
		out[p] = append(out[p], row.Int(s.Name))
	}
	return out, nil
}

// SequenceNext is the number after the highest taken (1 when none is).
func SequenceNext(taken []int64) int64 {
	var hi int64
	for _, t := range taken {
		if t > hi {
			hi = t
		}
	}
	return hi + 1
}

// sequenceTakenFor is SequenceTaken for rec's own parent.
func (r *Registry) sequenceTakenFor(ctx context.Context, rec *Record, s *model.Slot) []int64 {
	p := rec.Int(s.Sequence)
	if p == 0 {
		return nil
	}
	exclude := int64(0)
	if rec.Persisted() {
		exclude = rec.ID()
	}
	taken, err := r.SequenceTaken(ctx, rec.Class, s, []int64{p}, exclude)
	if err != nil {
		return nil
	}
	return taken[p]
}

// fillSequences numbers sequence slots: a new row, or one moved to another
// parent, gets the number after that parent's highest; otherwise a row keeps
// the number it was stored with.
func (r *Registry) fillSequences(ctx context.Context, rec *Record) {
	var stored *Record
	for _, s := range rec.Class.AllSlots() {
		if s.Sequence == "" || rec.Int(s.Sequence) == 0 {
			continue
		}
		if rec.Persisted() && stored == nil {
			if h, err := r.Class(rec.Class.Name); err == nil {
				stored, _ = h.Load(ctx, rec.ID())
			}
		}
		if stored != nil && stored.Int(s.Sequence) == rec.Int(s.Sequence) && stored.Values[s.Name] != nil {
			rec.Values[s.Name] = stored.Values[s.Name]
			continue
		}
		rec.Values[s.Name] = SequenceNext(r.sequenceTakenFor(ctx, rec, s))
	}
}

func contains64(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ApplyDefaults gives a new record's empty slots their default_expr values
// (reading its links, e.g. set.workout.sport). Front ends call it when
// showing a new record's form; Save calls it for records it inserts.
func (r *Registry) ApplyDefaults(ctx context.Context, rec *Record) {
	if rec.Persisted() {
		return
	}
	var env expr.Env
	for _, s := range rec.Class.AllSlots() {
		if s.DefaultExpr == nil || (rec.Values[s.Name] != nil && rec.Values[s.Name] != "") {
			continue
		}
		if env == nil {
			env = r.pathEnv(ctx, rec)
		}
		v, err := s.DefaultExpr.Eval(env)
		if err != nil || v == nil {
			continue
		}
		if cv, err := Coerce(s, exprToSlot(s, v)); err == nil {
			rec.Values[s.Name] = cv
		}
	}
}
