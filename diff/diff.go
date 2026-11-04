// Package diff compares two models and lists the schema operations that turn
// the first into the second.
package diff

import (
	"sort"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// Op is one schema change.
type Op struct {
	Op       string // create_table, drop_table, add_column, drop_column, modify_column, modify_column_default, add_unique, drop_unique
	Class    string
	Table    string
	Column   string
	Slot     *model.Slot
	FromType string
	ToType   string
	Change   string // "nullability" for nullability-only modify_column
	Columns  []string
	Name     string
	Warning  string
}

// Destructive reports whether applying the op can lose data.
func (o Op) Destructive() bool {
	return o.Op == "drop_table" || o.Op == "drop_column"
}

// Compare returns the operations to migrate from -> to, in a stable order.
func Compare(from, to *model.Model) []Op {
	var ops []Op
	for _, c := range from.Classes() {
		if to.Class(c.Name) == nil && !c.Abstract {
			ops = append(ops, Op{Op: "drop_table", Class: c.Name, Table: c.EffectiveTable(),
				Warning: "DESTRUCTIVE: drops all data in this table"})
		}
	}
	for _, c := range to.ConcreteClasses() {
		if from.Class(c.Name) == nil {
			ops = append(ops, Op{Op: "create_table", Class: c.Name, Table: c.EffectiveTable()})
		}
	}
	for _, tc := range to.Classes() {
		fc := from.Class(tc.Name)
		if fc == nil || tc.Abstract {
			continue
		}
		ops = append(ops, compareClass(fc, tc)...)
	}
	return ops
}

func compareClass(fc, tc *model.Class) []Op {
	var ops []Op
	table := tc.EffectiveTable()
	fromSlots := slotMap(fc)
	toSlots := slotMap(tc)

	for _, n := range sortedKeys(fromSlots) {
		if _, ok := toSlots[n]; !ok {
			ops = append(ops, Op{Op: "drop_column", Table: table, Column: n,
				Warning: "DESTRUCTIVE: drops column and all its data"})
		}
	}
	for _, n := range sortedKeys(toSlots) {
		if _, ok := fromSlots[n]; !ok {
			ops = append(ops, Op{Op: "add_column", Table: table, Column: n, Slot: toSlots[n]})
		}
	}
	for _, n := range sortedKeys(toSlots) {
		fs, ok := fromSlots[n]
		if !ok {
			continue
		}
		ts := toSlots[n]
		switch {
		case fs.Type != ts.Type:
			ops = append(ops, Op{Op: "modify_column", Table: table, Column: n, FromType: fs.Type, ToType: ts.Type,
				Slot: ts, Warning: "Type change may cause data loss"})
		case fs.Required != ts.Required:
			ops = append(ops, Op{Op: "modify_column", Table: table, Column: n, Change: "nullability", Slot: ts})
		case !defaultsEqual(fs.Default, ts.Default):
			ops = append(ops, Op{Op: "modify_column_default", Table: table, Column: n, Slot: ts})
		}
	}

	fromUQ := uniqueMap(fc)
	toUQ := uniqueMap(tc)
	for _, k := range sortedKeys(fromUQ) {
		if _, ok := toUQ[k]; !ok {
			c := fromUQ[k]
			ops = append(ops, Op{Op: "drop_unique", Table: table, Columns: c.Columns, Name: c.EffectiveName(table)})
		}
	}
	for _, k := range sortedKeys(toUQ) {
		if _, ok := fromUQ[k]; !ok {
			c := toUQ[k]
			ops = append(ops, Op{Op: "add_unique", Table: table, Columns: c.Columns, Name: c.EffectiveName(table)})
		}
	}
	return ops
}

// slotMap returns the columns stored in the class's own table. (The Perl
// comparator used all_slots, which reported inherited columns as missing from
// joined-table children.)
func slotMap(c *model.Class) map[string]*model.Slot {
	out := map[string]*model.Slot{}
	slots := c.AllSlots()
	if c.IsJoinedChild() {
		slots = c.Slots
	}
	for _, s := range slots {
		out[s.Name] = s
	}
	return out
}

func uniqueMap(c *model.Class) map[string]*model.Constraint {
	out := map[string]*model.Constraint{}
	for _, k := range c.UniqueConstraints() {
		cols := append([]string(nil), k.Columns...)
		sort.Strings(cols)
		out[strings.Join(cols, ",")] = k
	}
	return out
}

func defaultsEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
