// Package sandbox stands up a throwaway database for a schema: apply the DDL,
// load fixtures, run tests, tear down. SQLite in a temp directory by default.
package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/timeofmind/data-driven-framework-go/model"
	"github.com/timeofmind/data-driven-framework-go/runtime"
	"github.com/timeofmind/data-driven-framework-go/schema"
)

// Environment is a live sandbox.
type Environment struct {
	Registry *runtime.Registry
	Dir      string
}

// New creates a SQLite sandbox for an already-loaded model.
func New(m *model.Model) (*Environment, error) {
	dir, err := os.MkdirTemp("", "ddframework-sandbox-")
	if err != nil {
		return nil, err
	}
	reg, err := runtime.Open(m, "sqlite", filepath.Join(dir, "sandbox.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if _, err := reg.EnsureSchema(context.Background()); err != nil {
		reg.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	return &Environment{Registry: reg, Dir: dir}, nil
}

// FromSchema loads schema files and creates a sandbox for them.
func FromSchema(paths ...string) (*Environment, error) {
	m, err := schema.LoadFiles(paths...)
	if err != nil {
		return nil, err
	}
	return New(m)
}

// Close closes the database and deletes the sandbox directory.
func (e *Environment) Close() error {
	err := e.Registry.Close()
	os.RemoveAll(e.Dir)
	return err
}

// Fixtures maps class names to lists of records.
type Fixtures map[string][]map[string]any

// LoadFile reads YAML or JSON fixtures and inserts them.
func (e *Environment) LoadFile(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var data Fixtures
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		err = yaml.Unmarshal(b, &data)
	case ".json":
		err = json.Unmarshal(b, &data)
	default:
		return fmt.Errorf("fixtures: unrecognized file format for %s (expected .yaml or .json)", path)
	}
	if err != nil {
		return fmt.Errorf("fixtures %s: %w", path, err)
	}
	return e.Load(data)
}

// Load inserts fixture records class by class in dependency order, so
// belongs_to targets exist before the rows that reference them.
func (e *Environment) Load(data Fixtures) error {
	ctx := context.Background()
	for _, c := range e.Registry.Model.ConcreteClasses() {
		rows, ok := data[c.Name]
		if !ok {
			continue
		}
		h, err := e.Registry.Class(c.Name)
		if err != nil {
			return err
		}
		for i, row := range rows {
			rec := h.New(nil)
			for k, v := range row {
				if err := rec.Set(k, v); err != nil {
					return fmt.Errorf("fixtures %s[%d]: %w", c.Name, i, err)
				}
			}
			if err := h.Save(ctx, rec); err != nil {
				return fmt.Errorf("fixtures %s[%d]: %w", c.Name, i, err)
			}
		}
	}
	for name := range data {
		if _, err := e.Registry.Class(name); err != nil {
			return fmt.Errorf("fixtures: unknown class %q", name)
		}
	}
	return nil
}

// ClearAll deletes every row from every class table, children first.
func (e *Environment) ClearAll() error {
	ctx := context.Background()
	classes := e.Registry.Model.ConcreteClasses()
	q := e.Registry.Dialect.Quote
	for i := len(classes) - 1; i >= 0; i-- {
		if _, err := e.Registry.DB.ExecContext(ctx, "DELETE FROM "+q(classes[i].EffectiveTable())); err != nil {
			return err
		}
	}
	return nil
}

// With runs fn in a fresh sandbox for the schema and fails the test on setup errors.
func With(t testing.TB, schemaPath string, fn func(*Environment)) {
	t.Helper()
	env, err := FromSchema(schemaPath)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	defer env.Close()
	fn(env)
}
