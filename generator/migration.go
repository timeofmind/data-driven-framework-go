package generator

import (
	"fmt"
	"strings"
	"time"

	"github.com/timeofmind/data-driven-framework-go/dialect"
	"github.com/timeofmind/data-driven-framework-go/diff"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// Migration renders the diff between two models as SQL and as a summary.
type Migration struct {
	From, To *model.Model
	Dialect  dialect.Dialect
	Ops      []diff.Op
	Now      func() time.Time // for deterministic tests; defaults to time.Now
}

func NewMigration(from, to *model.Model, d dialect.Dialect) *Migration {
	return &Migration{From: from, To: to, Dialect: d, Ops: diff.Compare(from, to), Now: time.Now}
}

// SQL returns the migration script, or "" when nothing changed.
func (g *Migration) SQL() string {
	if len(g.Ops) == 0 {
		return ""
	}
	d := g.Dialect
	fv, tv := g.From.SchemaVersion, g.To.SchemaVersion
	hash := g.To.Hash()
	var b strings.Builder
	fmt.Fprintf(&b, `-- ============================================================
-- Migration: %s -> %s (%s)
-- Generated: %s
-- ============================================================
-- Review this file carefully before applying.
-- WARNING comments indicate potentially destructive changes.
-- ============================================================

%s
`, fv, tv, d.Name(), g.Now().Format("2006-01-02 15:04:05"), d.Preamble())
	for _, op := range g.Ops {
		b.WriteString(g.opSQL(op))
		b.WriteByte('\n')
	}
	b.WriteString(d.Postamble())
	fmt.Fprintf(&b, `
-- Record migration
INSERT INTO %[1]s
  (schema_version, framework_version, applied_at, model_hash, notes)
VALUES
  ('%[3]s', '%[5]s', %[6]s, '%[4]s', 'migration from %[2]s');

INSERT INTO %[7]s
  (from_version, to_version, applied_at, migration_sql, checksum)
VALUES
  ('%[2]s', '%[3]s', %[6]s, 'see file', '%[4]s');
`, d.Quote("_framework_schema_version"), fv, tv, hash, Version, d.Now(), d.Quote("_framework_migrations"))
	return b.String()
}

func (g *Migration) opSQL(op diff.Op) string {
	d := g.Dialect
	q := d.Quote
	warn := ""
	if op.Warning != "" {
		warn = "-- WARNING: " + op.Warning + "\n"
	}
	switch op.Op {
	case "create_table":
		return d.CreateTable(g.To.Class(op.Class), g.To)
	case "drop_table":
		return warn + "DROP TABLE IF EXISTS " + q(op.Table) + ";\n"
	case "add_column":
		def := d.ColumnDef(op.Slot, g.To)
		if _, sqlite := d.(dialect.SQLite); sqlite && op.Slot.Required && op.Slot.Default == nil {
			warn += "-- WARNING: SQLite cannot add a NOT NULL column without a default to a non-empty table\n"
		}
		return warn + "ALTER TABLE " + q(op.Table) + " ADD COLUMN " + def + ";\n"
	case "drop_column":
		return warn + "ALTER TABLE " + q(op.Table) + " DROP COLUMN " + q(op.Column) + ";\n"
	case "modify_column", "modify_column_default":
		if _, sqlite := d.(dialect.SQLite); sqlite {
			return warn + "-- SQLite cannot MODIFY COLUMN; rebuild " + q(op.Table) + " to change " + q(op.Column) +
				" to: " + d.ColumnDef(op.Slot, g.To) + "\n"
		}
		return warn + "ALTER TABLE " + q(op.Table) + " MODIFY COLUMN " + d.ColumnDef(op.Slot, g.To) + ";\n"
	case "add_unique":
		cols := quoted(d, op.Columns)
		if _, sqlite := d.(dialect.SQLite); sqlite {
			return "CREATE UNIQUE INDEX " + q(op.Name) + " ON " + q(op.Table) + " (" + cols + ");\n"
		}
		return "ALTER TABLE " + q(op.Table) + " ADD UNIQUE KEY " + q(op.Name) + " (" + cols + ");\n"
	case "drop_unique":
		if _, sqlite := d.(dialect.SQLite); sqlite {
			return "DROP INDEX IF EXISTS " + q(op.Name) + ";\n"
		}
		return "ALTER TABLE " + q(op.Table) + " DROP INDEX " + q(op.Name) + ";\n"
	}
	return "-- Unknown operation: " + op.Op + "\n"
}

// Summary returns a Markdown summary of the migration.
func (g *Migration) Summary() string {
	lines := []string{fmt.Sprintf("# Migration Summary: %s → %s\n", g.From.SchemaVersion, g.To.SchemaVersion)}
	if len(g.Ops) == 0 {
		return strings.Join(append(lines, "_No changes detected._"), "\n")
	}
	groups := map[string][]diff.Op{}
	for _, op := range g.Ops {
		key := op.Op
		if strings.HasPrefix(key, "modify") {
			key = "modify"
		}
		groups[key] = append(groups[key], op)
	}
	section := func(title, key string, line func(diff.Op) string) {
		if len(groups[key]) == 0 {
			return
		}
		lines = append(lines, "\n## "+title+"\n")
		for _, op := range groups[key] {
			lines = append(lines, line(op))
		}
	}
	section("New Tables", "create_table", func(o diff.Op) string { return "- `" + o.Table + "` (class: " + o.Class + ")" })
	section("⚠ Dropped Tables (DESTRUCTIVE)", "drop_table", func(o diff.Op) string { return "- `" + o.Table + "` — " + o.Warning })
	section("Added Columns", "add_column", func(o diff.Op) string { return "- `" + o.Table + "`.`" + o.Column + "`" })
	section("⚠ Dropped Columns (DESTRUCTIVE)", "drop_column", func(o diff.Op) string {
		return "- `" + o.Table + "`.`" + o.Column + "` — " + o.Warning
	})
	section("Modified Columns", "modify", func(o diff.Op) string {
		s := "- `" + o.Table + "`.`" + o.Column + "`"
		if o.Warning != "" {
			s += " ⚠ " + o.Warning
		}
		return s
	})
	section("Unique Constraints", "add_unique", func(o diff.Op) string { return "- add `" + o.Name + "`" })
	section("Dropped Unique Constraints", "drop_unique", func(o diff.Op) string { return "- drop `" + o.Name + "`" })
	return strings.Join(lines, "\n")
}

func quoted(d dialect.Dialect, cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = d.Quote(c)
	}
	return strings.Join(q, ", ")
}
