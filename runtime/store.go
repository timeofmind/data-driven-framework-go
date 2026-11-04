package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// ErrNotFound is returned by Load when no row has the given id.
var ErrNotFound = errors.New("record not found")

// Handle gives data access to one concrete class.
type Handle struct {
	reg   *Registry
	Class *model.Class
}

// Cond is one WHERE condition. Op is one of = != < <= > >= like in null notnull.
type Cond struct {
	Field string
	Op    string
	Value any
}

func Eq(field string, v any) Cond { return Cond{Field: field, Op: "=", Value: v} }

// Query selects records. Where conditions are ANDed; each AnyOf group is ANDed
// internally, the groups ORed together, and the result ANDed with Where.
type Query struct {
	Where   []Cond
	AnyOf   [][]Cond
	OrderBy string // "slot [asc|desc], ..."; defaults to the class order_by, then pk
	Limit   int
	Offset  int
}

// segment is one table in a class's joined-table inheritance chain.
type segment struct {
	class *model.Class
	table string
	slots []*model.Slot
}

func chain(c *model.Class) []segment {
	if c.IsJoinedChild() {
		return append(chain(c.ParentClass), segment{c, c.EffectiveTable(), c.Slots})
	}
	return []segment{{c, c.EffectiveTable(), c.AllSlots()}}
}

func (h *Handle) pk() (*model.Slot, error) {
	pk := h.Class.PKSlot()
	if pk == nil {
		return nil, fmt.Errorf("%s has no primary key slot", h.Class.Name)
	}
	return pk, nil
}

// New returns an unsaved record of this class with defaults applied.
func (h *Handle) New(values map[string]any) *Record { return NewRecord(h.Class, values) }

// columnRefs maps slot names to qualified column references (t0.col, t1.col …).
func (h *Handle) columnRefs() (map[string]string, []*model.Slot) {
	q := h.reg.Dialect.Quote
	refs := map[string]string{}
	var order []*model.Slot
	for i, seg := range chain(h.Class) {
		for _, s := range seg.slots {
			if _, dup := refs[s.Name]; dup {
				continue
			}
			refs[s.Name] = fmt.Sprintf("t%d.%s", i, q(s.Name))
			order = append(order, s)
		}
	}
	return refs, order
}

func (h *Handle) fromClause() (string, error) {
	q := h.reg.Dialect.Quote
	segs := chain(h.Class)
	from := q(segs[0].table) + " t0"
	if len(segs) == 1 {
		return from, nil // join tables (no pk) are fine to list and count
	}
	pk, err := h.pk()
	if err != nil {
		return "", err
	}
	for i := 1; i < len(segs); i++ {
		from += fmt.Sprintf(" JOIN %s t%d ON t%d.%s = t0.%s", q(segs[i].table), i, i, q(pk.Name), q(pk.Name))
	}
	return from, nil
}

func (h *Handle) buildWhere(qr Query, refs map[string]string) (string, []any, error) {
	var clauses []string
	var args []any
	render := func(c Cond) (string, error) {
		col, ok := refs[c.Field]
		if !ok {
			return "", fmt.Errorf("%s has no field %q", h.Class.Name, c.Field)
		}
		op := strings.ToLower(c.Op)
		switch op {
		case "=", "!=", "<", "<=", ">", ">=":
			if c.Value == nil {
				if op == "=" {
					return col + " IS NULL", nil
				}
				if op == "!=" {
					return col + " IS NOT NULL", nil
				}
			}
			args = append(args, dbValue(c.Value))
			return col + " " + op + " ?", nil
		case "like":
			args = append(args, c.Value)
			return col + " LIKE ?", nil
		case "null":
			return col + " IS NULL", nil
		case "notnull":
			return col + " IS NOT NULL", nil
		case "in":
			vals, ok := c.Value.([]any)
			if !ok || len(vals) == 0 {
				return "1=0", nil
			}
			ph := make([]string, len(vals))
			for i, v := range vals {
				ph[i] = "?"
				args = append(args, dbValue(v))
			}
			return col + " IN (" + strings.Join(ph, ", ") + ")", nil
		}
		return "", fmt.Errorf("unsupported operator %q", c.Op)
	}
	for _, c := range qr.Where {
		s, err := render(c)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, s)
	}
	if len(qr.AnyOf) > 0 {
		var groups []string
		for _, g := range qr.AnyOf {
			var parts []string
			for _, c := range g {
				s, err := render(c)
				if err != nil {
					return "", nil, err
				}
				parts = append(parts, s)
			}
			if len(parts) == 0 {
				groups = append(groups, "1=1")
			} else {
				groups = append(groups, "("+strings.Join(parts, " AND ")+")")
			}
		}
		clauses = append(clauses, "("+strings.Join(groups, " OR ")+")")
	}
	if len(clauses) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func (h *Handle) orderClause(spec string, refs map[string]string) (string, error) {
	if spec == "" {
		spec = h.Class.OrderBy
	}
	pk, _ := h.pk()
	var parts []string
	if spec != "" {
		for _, term := range strings.Split(spec, ",") {
			f := strings.Fields(term)
			if len(f) == 0 {
				continue
			}
			col, ok := refs[f[0]]
			if !ok {
				return "", fmt.Errorf("%s has no field %q to order by", h.Class.Name, f[0])
			}
			dir := "ASC"
			if len(f) > 1 && strings.EqualFold(f[1], "desc") {
				dir = "DESC"
			}
			parts = append(parts, col+" "+dir)
		}
	}
	if pk != nil {
		parts = append(parts, refs[pk.Name]+" ASC") // stable tiebreak
	}
	if len(parts) == 0 {
		return "", nil
	}
	return " ORDER BY " + strings.Join(parts, ", "), nil
}

// List returns records matching the query.
func (h *Handle) List(ctx context.Context, qr Query) ([]*Record, error) {
	refs, slots := h.columnRefs()
	from, err := h.fromClause()
	if err != nil {
		return nil, err
	}
	where, args, err := h.buildWhere(qr, refs)
	if err != nil {
		return nil, err
	}
	order, err := h.orderClause(qr.OrderBy, refs)
	if err != nil {
		return nil, err
	}
	cols := make([]string, len(slots))
	for i, s := range slots {
		cols[i] = refs[s.Name]
	}
	query := "SELECT " + strings.Join(cols, ", ") + " FROM " + from + where + order
	if qr.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", qr.Limit)
		if qr.Offset > 0 {
			query += fmt.Sprintf(" OFFSET %d", qr.Offset)
		}
	}
	rows, err := h.reg.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s list: %w", h.Class.Name, err)
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		dest := make([]any, len(slots))
		ptrs := make([]any, len(slots))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := &Record{Class: h.Class, Values: map[string]any{}, persisted: true}
		for i, s := range slots {
			rec.Values[s.Name] = fromDB(s, dest[i])
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Count returns how many records match the query (ignoring order and paging).
func (h *Handle) Count(ctx context.Context, qr Query) (int, error) {
	refs, _ := h.columnRefs()
	from, err := h.fromClause()
	if err != nil {
		return 0, err
	}
	where, args, err := h.buildWhere(qr, refs)
	if err != nil {
		return 0, err
	}
	var n int
	err = h.reg.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+from+where, args...).Scan(&n)
	return n, err
}

// First returns the first match, or ErrNotFound.
func (h *Handle) First(ctx context.Context, qr Query) (*Record, error) {
	qr.Limit = 1
	recs, err := h.List(ctx, qr)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrNotFound
	}
	return recs[0], nil
}

// Load fetches one record by primary key.
func (h *Handle) Load(ctx context.Context, id int64) (*Record, error) {
	pk, err := h.pk()
	if err != nil {
		return nil, err
	}
	return h.First(ctx, Query{Where: []Cond{Eq(pk.Name, id)}})
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Save validates and inserts or updates the record (all inheritance tables in
// one transaction), hashing any plaintext password values first. The record is
// reloaded afterwards so autogenerated values are filled in.
func (h *Handle) Save(ctx context.Context, rec *Record) error {
	if rec.Class != h.Class {
		return fmt.Errorf("save: record is a %s, handle is %s", rec.Class.Name, h.Class.Name)
	}
	if errs := rec.Validate(); errs != nil {
		return errs
	}
	for _, s := range h.Class.AllSlots() {
		if s.Type != "password" {
			continue
		}
		if plain, _ := rec.Values[s.Name].(string); plain != "" && !strings.HasPrefix(plain, bcryptPrefix) {
			hash, err := HashPassword(plain)
			if err != nil {
				return err
			}
			rec.Values[s.Name] = hash
		}
	}
	pk, err := h.pk()
	if err != nil {
		return err
	}
	err = h.reg.Tx(ctx, func(tx *sql.Tx) error {
		if rec.persisted && rec.ID() != 0 {
			return h.update(ctx, tx, rec, pk)
		}
		return h.insert(ctx, tx, rec, pk)
	})
	if err != nil {
		return err
	}
	fresh, err := h.Load(ctx, rec.ID())
	if err != nil {
		return err
	}
	rec.Values, rec.persisted = fresh.Values, true
	return nil
}

func (h *Handle) insert(ctx context.Context, tx execer, rec *Record, pk *model.Slot) error {
	q := h.reg.Dialect.Quote
	var id int64
	for i, seg := range chain(h.Class) {
		var cols []string
		var args []any
		if i > 0 {
			cols, args = append(cols, q(pk.Name)), append(args, id)
		}
		for _, s := range seg.slots {
			if s.Autogenerated {
				continue
			}
			cols = append(cols, q(s.Name))
			args = append(args, dbValue(rec.Values[s.Name]))
		}
		var stmt string
		if len(cols) == 0 {
			if h.reg.Dialect.Name() == "sqlite" {
				stmt = "INSERT INTO " + q(seg.table) + " DEFAULT VALUES"
			} else {
				stmt = "INSERT INTO " + q(seg.table) + " () VALUES ()"
			}
		} else {
			stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", q(seg.table), strings.Join(cols, ", "),
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", "))
		}
		res, err := tx.ExecContext(ctx, stmt, args...)
		if err != nil {
			return fmt.Errorf("%s insert: %w", h.Class.Name, err)
		}
		if i == 0 {
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		}
	}
	rec.Values[pk.Name] = id
	return nil
}

func (h *Handle) update(ctx context.Context, tx execer, rec *Record, pk *model.Slot) error {
	q := h.reg.Dialect.Quote
	for _, seg := range chain(h.Class) {
		var sets []string
		var args []any
		for _, s := range seg.slots {
			if s.Autogenerated {
				if s.Type == "datetime" && s.Name == "updated_at" {
					sets = append(sets, q(s.Name)+" = "+h.reg.Dialect.Now())
				}
				continue
			}
			sets = append(sets, q(s.Name)+" = ?")
			args = append(args, dbValue(rec.Values[s.Name]))
		}
		if len(sets) == 0 {
			continue
		}
		args = append(args, rec.ID())
		stmt := fmt.Sprintf("UPDATE %s SET %s WHERE %s = ?", q(seg.table), strings.Join(sets, ", "), q(pk.Name))
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("%s update: %w", h.Class.Name, err)
		}
	}
	return nil
}

// Delete removes the record with the given id from every inheritance table.
func (h *Handle) Delete(ctx context.Context, id int64) error {
	pk, err := h.pk()
	if err != nil {
		return err
	}
	q := h.reg.Dialect.Quote
	segs := chain(h.Class)
	return h.reg.Tx(ctx, func(tx *sql.Tx) error {
		for i := len(segs) - 1; i >= 0; i-- {
			stmt := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", q(segs[i].table), q(pk.Name))
			if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
				return fmt.Errorf("%s delete: %w", h.Class.Name, err)
			}
		}
		return nil
	})
}

// Parent follows a belongs_to relationship (by relationship name or FK slot).
func (h *Handle) Parent(ctx context.Context, rec *Record, rel string) (*Record, error) {
	r := h.Class.Relationship(rel)
	if r == nil {
		r = h.Class.BelongsToVia(rel)
	}
	if r == nil || !r.IsBelongsTo() {
		return nil, fmt.Errorf("%s has no belongs_to %q", h.Class.Name, rel)
	}
	id := rec.Int(r.Via)
	if id == 0 {
		return nil, ErrNotFound
	}
	t, err := h.reg.Class(r.Target)
	if err != nil {
		return nil, err
	}
	return t.Load(ctx, id)
}

// Children follows a has_many or many_to_many relationship. Extra conditions
// and paging come from qr; ordering defaults to the relationship's order_by.
func (h *Handle) Children(ctx context.Context, rec *Record, rel string, qr Query) ([]*Record, error) {
	r := h.Class.Relationship(rel)
	if r == nil {
		return nil, fmt.Errorf("%s has no relationship %q", h.Class.Name, rel)
	}
	t, err := h.reg.Class(r.Target)
	if err != nil {
		return nil, err
	}
	if qr.OrderBy == "" {
		qr.OrderBy = r.OrderBy
	}
	switch {
	case r.IsHasMany():
		via := r.Via
		if via == "" {
			via = inverseFK(t.Class, h.Class.Name)
		}
		if via == "" {
			return nil, fmt.Errorf("%s.%s: cannot determine FK on %s", h.Class.Name, rel, r.Target)
		}
		qr.Where = append(qr.Where, Eq(via, rec.ID()))
		return t.List(ctx, qr)
	case r.IsManyToMany():
		j, err := h.reg.Class(r.Through)
		if err != nil {
			return nil, err
		}
		own, other := inverseFK(j.Class, h.Class.Name), inverseFK(j.Class, r.Target)
		if own == "" || other == "" {
			return nil, fmt.Errorf("%s.%s: join class %s lacks belongs_to links", h.Class.Name, rel, r.Through)
		}
		links, err := j.List(ctx, Query{Where: []Cond{Eq(own, rec.ID())}})
		if err != nil {
			return nil, err
		}
		ids := make([]any, 0, len(links))
		for _, l := range links {
			ids = append(ids, l.Int(other))
		}
		tpk, err := t.pk()
		if err != nil {
			return nil, err
		}
		qr.Where = append(qr.Where, Cond{Field: tpk.Name, Op: "in", Value: ids})
		return t.List(ctx, qr)
	}
	return nil, fmt.Errorf("%s.%s is not a to-many relationship", h.Class.Name, rel)
}

func inverseFK(c *model.Class, target string) string {
	for _, r := range c.AllRelationships() {
		if r.IsBelongsTo() && r.Target == target {
			return r.Via
		}
	}
	return ""
}

// dbValue converts a normalised value to a driver argument.
func dbValue(v any) any {
	if b, ok := v.(bool); ok {
		if b {
			return int64(1)
		}
		return int64(0)
	}
	return v
}

// fromDB normalises a scanned driver value to the slot's Go type.
func fromDB(s *model.Slot, v any) any {
	if v == nil {
		return nil
	}
	out, err := Coerce(s, v)
	if err != nil {
		if b, ok := v.([]byte); ok {
			return string(b)
		}
		return v
	}
	return out
}
