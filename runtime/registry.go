package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/timeofmind/data-driven-framework-go/dialect"
	"github.com/timeofmind/data-driven-framework-go/model"
	"github.com/timeofmind/data-driven-framework-go/schema"
)

// Registry binds a model to a database.
type Registry struct {
	Model   *model.Model
	DB      *sql.DB
	Dialect dialect.Dialect
	handles map[string]*Handle
}

// New wraps an existing *sql.DB.
func New(m *model.Model, db *sql.DB, d dialect.Dialect) *Registry {
	r := &Registry{Model: m, DB: db, Dialect: d, handles: map[string]*Handle{}}
	for _, c := range m.ConcreteClasses() {
		r.handles[c.Name] = &Handle{reg: r, Class: c}
	}
	return r
}

// Open connects to a database. driver is "mysql" or "sqlite". For MySQL the DSN
// is a go-sql-driver DSN (user:pass@unix(/run/mysqld/mysqld.sock)/db); for
// SQLite it is a file path or ":memory:". Foreign keys are enabled on SQLite.
func Open(m *model.Model, driver, dsn string) (*Registry, error) {
	d, err := dialect.ByName(driver)
	if err != nil {
		return nil, err
	}
	if d.Name() == "sqlite" {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	} else if !strings.Contains(dsn, "charset=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "charset=utf8mb4"
	}
	db, err := sql.Open(d.DriverName(), dsn)
	if err != nil {
		return nil, err
	}
	if d.Name() == "sqlite" && strings.HasPrefix(dsn, ":memory:") {
		db.SetMaxOpenConns(1) // each connection would otherwise get its own empty DB
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return New(m, db, d), nil
}

// FromSchema loads schema files and opens the database in one step.
func FromSchema(driver, dsn string, paths ...string) (*Registry, error) {
	m, err := schema.LoadFiles(paths...)
	if err != nil {
		return nil, err
	}
	return Open(m, driver, dsn)
}

func (r *Registry) Close() error { return r.DB.Close() }

// Class returns the handle for a concrete class.
func (r *Registry) Class(name string) (*Handle, error) {
	h, ok := r.handles[name]
	if !ok {
		return nil, fmt.Errorf("registry: no class %q (is it abstract or not in schema?)", name)
	}
	return h, nil
}

// MustClass is Class that panics; for use with names known at compile time.
func (r *Registry) MustClass(name string) *Handle {
	h, err := r.Class(name)
	if err != nil {
		panic(err)
	}
	return h
}

func (r *Registry) ClassNames() []string {
	var out []string
	for _, c := range r.Model.ConcreteClasses() {
		out = append(out, c.Name)
	}
	return out
}

// Tx runs fn inside a transaction.
func (r *Registry) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

const bcryptPrefix = "$2"

// HashPassword returns a bcrypt hash of a plaintext password.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword reports whether plain matches the record's password slot.
func CheckPassword(rec *Record, slot, plain string) bool {
	hash, _ := rec.Values[slot].(string)
	if !strings.HasPrefix(hash, bcryptPrefix) {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
