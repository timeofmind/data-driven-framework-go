// Command ddframework validates schemas and generates artifacts from them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/dialect"
	"github.com/timeofmind/data-driven-framework-go/generator"
	"github.com/timeofmind/data-driven-framework-go/model"
	"github.com/timeofmind/data-driven-framework-go/reverse"
	"github.com/timeofmind/data-driven-framework-go/runtime"
	"github.com/timeofmind/data-driven-framework-go/sandbox"
	"github.com/timeofmind/data-driven-framework-go/schema"
)

type command struct {
	summary string
	usage   string
	run     func(args []string) error
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"validate":             {"Validate a schema file", "--schema FILE [--schema FILE...]", cmdValidate},
		"generate-sql":         {"Generate DDL (MySQL or SQLite)", "--schema FILE [--dialect mysql|sqlite] [--output FILE]", cmdGenerateSQL},
		"reverse-from-db":      {"Generate YAML schema from an existing database", "--driver mysql|sqlite --dsn DSN [--output FILE]", cmdReverse},
		"generate-openapi":     {"Generate OpenAPI 3.0 spec", "--schema FILE [--format yaml|json] [--title T] [--base-path /api/v1] [--output FILE]", cmdOpenAPI},
		"generate-json-schema": {"Generate JSON Schema definitions", "--schema FILE [--output FILE]", cmdJSONSchema},
		"generate-ui":          {"Generate UI scaffold (JSON config + HTML)", "--schema FILE --output DIR", cmdUI},
		"generate-docs":        {"Generate Markdown documentation", "--schema FILE [--output FILE]", cmdDocs},
		"diff":                 {"Compare two schema versions", "--from FILE --to FILE [--sql] [--dialect mysql|sqlite] [--output FILE]", cmdDiff},
		"migrate":              {"Apply a migration SQL file", "--driver mysql|sqlite --dsn DSN --file FILE", cmdMigrate},
		"ensure-schema":        {"Create missing tables/columns in a database (never drops)", "--schema FILE --driver mysql|sqlite --dsn DSN", cmdEnsure},
		"init-project":         {"Initialize a new project skeleton", "--name NAME [--dir DIR]", cmdInit},
		"sandbox-info":         {"Load a schema (+ fixtures) into a temp SQLite sandbox and report", "--schema FILE [--fixtures FILE]", cmdSandbox},
		"version": {"Show version information", "", func([]string) error {
			fmt.Println("ddframework (data-driven-framework-go) v" + generator.Version)
			return nil
		}},
	}
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		if len(os.Args) > 2 {
			if c, ok := commands[os.Args[2]]; ok {
				fmt.Printf("ddframework %s %s\n\n%s\n", os.Args[2], c.usage, c.summary)
				return
			}
		}
		usage(os.Stdout)
		return
	}
	c, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "ddframework: unknown command %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err := c.run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "ddframework "+os.Args[1]+": "+err.Error())
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: ddframework <command> [options]\n\nCommands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-22s %s\n", n, commands[n].summary)
	}
	fmt.Fprintln(w, "\nRun: ddframework help <command> for details.")
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func loadModel(paths []string) (*model.Model, error) {
	if len(paths) == 0 {
		return nil, errors.New("--schema is required")
	}
	return schema.LoadFiles(paths...)
}

func output(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "wrote "+path)
	return nil
}

func cmdValidate(args []string) error {
	fs := flags("validate")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(schemas) == 0 {
		return errors.New("--schema is required")
	}
	src := make([]schema.Source, len(schemas))
	for i, p := range schemas {
		src[i] = schema.File(p)
	}
	m, err := schema.Parse(src...)
	if err != nil {
		return err
	}
	schema.Resolve(m)
	if problems := schema.Validate(m); len(problems) > 0 {
		for _, p := range problems {
			fmt.Println("  ✗ " + p)
		}
		return fmt.Errorf("%d problem(s)", len(problems))
	}
	fmt.Printf("✓ schema valid: %d classes, %d enums (version %s, hash %s)\n",
		len(m.ClassNames()), len(m.EnumNames()), m.SchemaVersion, m.Hash())
	return nil
}

func cmdGenerateSQL(args []string) error {
	fs := flags("generate-sql")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	dial := fs.String("dialect", "mysql", "mysql or sqlite")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	d, err := dialect.ByName(*dial)
	if err != nil {
		return err
	}
	return output(*out, []byte(generator.SQL(m, d)))
}

func cmdReverse(args []string) error {
	fs := flags("reverse-from-db")
	driver := fs.String("driver", "mysql", "mysql or sqlite")
	dsn := fs.String("dsn", "", "database DSN")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn is required")
	}
	reg, err := runtime.Open(model.New(), *driver, *dsn)
	if err != nil {
		return err
	}
	defer reg.Close()
	y, err := reverse.YAML(context.Background(), reg.DB, *driver)
	if err != nil {
		return err
	}
	return output(*out, y)
}

func cmdOpenAPI(args []string) error {
	fs := flags("generate-openapi")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	format := fs.String("format", "yaml", "yaml or json")
	title := fs.String("title", "", "API title")
	base := fs.String("base-path", "/api/v1", "base path")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	opts := generator.OpenAPIOptions{Title: *title, BasePath: *base}
	var b []byte
	if *format == "json" {
		b, err = generator.OpenAPIJSON(m, opts)
	} else {
		b, err = generator.OpenAPIYAML(m, opts)
	}
	if err != nil {
		return err
	}
	return output(*out, b)
}

func cmdJSONSchema(args []string) error {
	fs := flags("generate-json-schema")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	b, err := generator.JSONSchemaJSON(m)
	if err != nil {
		return err
	}
	return output(*out, append(b, '\n'))
}

func cmdUI(args []string) error {
	fs := flags("generate-ui")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	out := fs.String("output", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--output DIR is required")
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	files, err := generator.WriteUI(m, *out)
	if err != nil {
		return err
	}
	for _, f := range files {
		fmt.Println("wrote " + f)
	}
	return nil
}

func cmdDocs(args []string) error {
	fs := flags("generate-docs")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	return output(*out, []byte(generator.Docs(m)))
}

func cmdDiff(args []string) error {
	fs := flags("diff")
	from := fs.String("from", "", "old schema file")
	to := fs.String("to", "", "new schema file")
	asSQL := fs.Bool("sql", false, "print migration SQL instead of a summary")
	dial := fs.String("dialect", "mysql", "mysql or sqlite")
	out := fs.String("output", "", "output file (default stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return errors.New("--from and --to are required")
	}
	fm, err := schema.LoadFiles(*from)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	tm, err := schema.LoadFiles(*to)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}
	d, err := dialect.ByName(*dial)
	if err != nil {
		return err
	}
	mig := generator.NewMigration(fm, tm, d)
	if *asSQL {
		return output(*out, []byte(mig.SQL()))
	}
	return output(*out, []byte(mig.Summary()+"\n"))
}

func cmdMigrate(args []string) error {
	fs := flags("migrate")
	driver := fs.String("driver", "mysql", "mysql or sqlite")
	dsn := fs.String("dsn", "", "database DSN")
	file := fs.String("file", "", "migration SQL file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" || *file == "" {
		return errors.New("--dsn and --file are required")
	}
	script, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	reg, err := runtime.Open(model.New(), *driver, *dsn)
	if err != nil {
		return err
	}
	defer reg.Close()
	if err := reg.ApplySQL(context.Background(), string(script)); err != nil {
		return err
	}
	fmt.Println("✓ applied " + *file)
	return nil
}

func cmdEnsure(args []string) error {
	fs := flags("ensure-schema")
	var schemas multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	driver := fs.String("driver", "mysql", "mysql or sqlite")
	dsn := fs.String("dsn", "", "database DSN")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	reg, err := runtime.Open(m, *driver, *dsn)
	if err != nil {
		return err
	}
	defer reg.Close()
	res, err := reg.EnsureSchema(context.Background())
	if err != nil {
		return err
	}
	for _, t := range res.CreatedTables {
		fmt.Println("created table " + t)
	}
	for _, c := range res.AddedColumns {
		fmt.Println("added column " + c)
	}
	for _, p := range res.Pending {
		fmt.Println("pending (not applied): " + p)
	}
	if len(res.CreatedTables)+len(res.AddedColumns) == 0 {
		fmt.Println("✓ database already matches schema")
	}
	return nil
}

func cmdInit(args []string) error {
	fs := flags("init-project")
	name := fs.String("name", "", "project name")
	dir := fs.String("dir", "", "target directory (default ./NAME)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	root := *dir
	if root == "" {
		root = *name
	}
	files := map[string]string{
		"schema/" + *name + ".yaml": fmt.Sprintf(`schema_version: "1.0.0"
description: "%s schema"

enums:
  Status:
    values: [active, inactive]
    default: active

classes:
  Item:
    label: Item
    slots:
      - name: id
        type: integer
        autogenerated: true
        read_only: true
        required: true
      - name: name
        type: string
        required: true
        ui_order: 1
      - name: status
        type: enum
        enum: Status
        default: active
        ui_order: 2
      - name: created_at
        type: datetime
        autogenerated: true
        read_only: true
    constraints:
      unique: [name]
`, *name),
		"fixtures/seed.yaml": "Item:\n  - name: example\n",
		"generated/.gitkeep": "",
		"README.md":          "# " + *name + "\n\nSchema: `schema/" + *name + ".yaml`\n\n    ddframework validate --schema schema/" + *name + ".yaml\n    ddframework generate-sql --schema schema/" + *name + ".yaml --output generated/schema.sql\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			fmt.Println("exists, skipped " + p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Println("created " + p)
	}
	return nil
}

func cmdSandbox(args []string) error {
	fs := flags("sandbox-info")
	var schemas, fixtures multi
	fs.Var(&schemas, "schema", "schema file (repeatable)")
	fs.Var(&fixtures, "fixtures", "fixtures file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadModel(schemas)
	if err != nil {
		return err
	}
	env, err := sandbox.New(m)
	if err != nil {
		return err
	}
	defer env.Close()
	for _, f := range fixtures {
		if err := env.LoadFile(f); err != nil {
			return err
		}
	}
	ctx := context.Background()
	fmt.Printf("sandbox: %s (sqlite)\n", env.Dir)
	for _, n := range env.Registry.ClassNames() {
		h := env.Registry.MustClass(n)
		count, err := h.Count(ctx, runtime.Query{})
		if err != nil {
			return err
		}
		fmt.Printf("  %-24s %-24s %d rows\n", n, h.Class.EffectiveTable(), count)
	}
	return nil
}
