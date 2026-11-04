// Package dialect holds the per-database differences used by the SQL and
// migration generators and by the runtime: quoting, column types, DDL shape.
// MySQL/MariaDB is the reference dialect (as in the Perl framework); SQLite is
// supported for embedded use, tests and sandboxes.
package dialect

import (
	"fmt"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// Dialect describes one SQL database family.
type Dialect interface {
	Name() string
	// DriverName is the database/sql driver name to open with.
	DriverName() string
	Quote(ident string) string
	// CreateTable returns the full DDL for one concrete class, terminated by ";".
	CreateTable(c *model.Class, m *model.Model) string
	// ColumnDef returns the column definition for a slot (no leading indent,
	// no trailing comma, no PRIMARY KEY clause).
	ColumnDef(s *model.Slot, m *model.Model) string
	Preamble() string
	Postamble() string
	MetadataTables() string
	// Now is the SQL expression for the current timestamp.
	Now() string
}

// ByName returns the dialect for "mysql", "mariadb" or "sqlite".
func ByName(name string) (Dialect, error) {
	switch strings.ToLower(name) {
	case "mysql", "mariadb", "":
		return MySQL{}, nil
	case "sqlite", "sqlite3":
		return SQLite{}, nil
	}
	return nil, fmt.Errorf("dialect: unsupported database %q (want mysql or sqlite)", name)
}

func escapeString(s string) string { return strings.ReplaceAll(s, "'", "''") }

func literalDefault(s *model.Slot) string {
	d := *s.Default
	if s.Type == "integer" || s.Type == "float" || s.Type == "boolean" {
		return d
	}
	return "'" + escapeString(d) + "'"
}

func onDeleteClause(r *model.Relationship) string {
	switch r.OnDelete {
	case "cascade":
		return " ON DELETE CASCADE"
	case "set_null":
		return " ON DELETE SET NULL"
	case "restrict":
		return " ON DELETE RESTRICT"
	}
	return ""
}

// emittedSlots returns the slots stored in this class's own table: own slots
// for a joined-table child, all (inherited) slots otherwise.
func emittedSlots(c *model.Class) []*model.Slot {
	if c.IsJoinedChild() {
		return c.Slots
	}
	return c.AllSlots()
}

func pkName(c *model.Class) string {
	if pk := c.PKSlot(); pk != nil {
		return pk.Name
	}
	return "id"
}

func quoteList(d Dialect, cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.Quote(c)
	}
	return strings.Join(q, ", ")
}
