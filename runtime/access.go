package runtime

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// Principal is the signed-in identity a request acts as; nil means anonymous.
type Principal struct {
	ID    int64  // primary key of the auth-class record
	Name  string // display name
	Admin bool   // bypasses access rules
}

func readRule(c *model.Class) string {
	if c.Access == nil || c.Access.Read == "" {
		return "public"
	}
	return c.Access.Read
}

// WriteRule returns the effective write rule for a class.
func WriteRule(c *model.Class) string {
	if c.Access != nil && c.Access.Write != "" {
		return c.Access.Write
	}
	if c.Access != nil && c.Access.Owner != "" {
		return "owner"
	}
	return "authenticated"
}

func ownerSlot(c *model.Class) string {
	if c.Access == nil {
		return ""
	}
	return c.Access.Owner
}

func publicConds(c *model.Class) []Cond {
	if c.Access == nil {
		return nil
	}
	var out []Cond
	for _, k := range sortedKeys(c.Access.PublicWhen) {
		out = append(out, Cond{Field: k, Op: "=", Value: c.Access.PublicWhen[k]})
	}
	return out
}

// Scope narrows a query to the rows p may read. ok is false when p may read
// nothing at all (for example an anonymous visitor on an owner-only class).
func (h *Handle) Scope(qr Query, p *Principal) (Query, bool) {
	c := h.Class
	if p != nil && p.Admin {
		return qr, true
	}
	owner := ownerSlot(c)
	pub := publicConds(c)
	var groups [][]Cond
	switch readRule(c) {
	case "public":
		if len(pub) == 0 {
			return qr, true
		}
		groups = append(groups, pub)
	case "authenticated":
		if p != nil {
			return qr, true
		}
		if len(pub) == 0 {
			return qr, false
		}
		groups = append(groups, pub)
	case "owner":
		if len(pub) > 0 {
			groups = append(groups, pub)
		}
	}
	if p != nil && owner != "" {
		groups = append(groups, []Cond{Eq(owner, p.ID)})
	}
	if len(groups) == 0 {
		return qr, false
	}
	qr.AnyOf = append(qr.AnyOf, groups...)
	return qr, true
}

// CanRead reports whether p may read rec.
func CanRead(rec *Record, p *Principal) bool {
	c := rec.Class
	if p != nil && p.Admin {
		return true
	}
	if p != nil && ownerSlot(c) != "" && rec.Int(ownerSlot(c)) == p.ID {
		return true
	}
	pub := publicConds(c)
	matchesPublic := len(pub) > 0
	for _, k := range pub {
		if rec.String(k.Field) != FormatValue(k.Value) {
			matchesPublic = false
		}
	}
	switch readRule(c) {
	case "public":
		return len(pub) == 0 || matchesPublic
	case "authenticated":
		return p != nil || matchesPublic
	}
	return matchesPublic
}

// CanCreate reports whether p may create records of the class.
func CanCreate(c *model.Class, p *Principal) bool {
	if p != nil && p.Admin {
		return true
	}
	if c.Auth != nil {
		return false // principals are created through sign-up, not generic create
	}
	return WriteRule(c) != "nobody" && p != nil
}

// CanWrite reports whether p may update or delete rec.
func CanWrite(rec *Record, p *Principal) bool {
	if p == nil {
		return false
	}
	if p.Admin {
		return true
	}
	switch WriteRule(rec.Class) {
	case "nobody":
		return false
	case "authenticated":
		return true
	}
	owner := ownerSlot(rec.Class)
	return owner != "" && rec.Int(owner) == p.ID
}

// ErrBadCredentials is returned by Authenticate for an unknown login or wrong password.
var ErrBadCredentials = errors.New("invalid login or password")

// AuthClass returns the handle for the model's auth (principal) class.
func (r *Registry) AuthClass() (*Handle, error) {
	for _, c := range r.Model.ConcreteClasses() {
		if c.Auth != nil {
			return r.Class(c.Name)
		}
	}
	return nil, errors.New("schema has no class with an auth block")
}

// Authenticate finds the principal by login slot and checks the password.
// Logins are compared case-insensitively.
func (r *Registry) Authenticate(ctx context.Context, login, password string) (*Record, error) {
	h, err := r.AuthClass()
	if err != nil {
		return nil, err
	}
	a := h.Class.Auth
	rec, err := h.First(ctx, Query{Where: []Cond{Eq(a.Login, strings.ToLower(strings.TrimSpace(login)))}})
	if errors.Is(err, ErrNotFound) {
		// Spend similar time as a real comparison so lookups don't leak which logins exist.
		CheckPassword(&Record{Values: map[string]any{"p": dummyHash}}, "p", password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if !CheckPassword(rec, a.Password, password) {
		return nil, ErrBadCredentials
	}
	return rec, nil
}

// PrincipalFor builds a Principal from an auth-class record.
func PrincipalFor(rec *Record) *Principal {
	p := &Principal{ID: rec.ID()}
	if a := rec.Class.Auth; a != nil && a.Display != "" {
		p.Name = rec.String(a.Display)
	}
	if p.Name == "" {
		p.Name = rec.Title()
	}
	return p
}

// A valid bcrypt hash of a random string, used to equalise timing.
const dummyHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5BWX4Z3Ng0QKqMgPvFuoeE.Fsu8W."

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
