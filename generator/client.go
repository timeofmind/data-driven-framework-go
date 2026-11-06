package generator

import (
	"encoding/json"
	"sort"

	"github.com/timeofmind/data-driven-framework-go/expr"
	"github.com/timeofmind/data-driven-framework-go/model"
)

// ClientClass is the part of a class's rules a browser can apply while a form
// is filled in (see the README, "Rules in the browser"). Expressions are
// shipped as trees (expr.Tree); the server stays the authority on every rule.
type ClientClass struct {
	// Types of every own slot the rules read, so the browser can turn form
	// input into expression values (integer, float, duration, boolean, …).
	Types  map[string]string        `json:"types"`
	Fields map[string]*ClientField  `json:"fields,omitempty"`
	Match  map[string][]ClientMatch `json:"match,omitempty"` // by FK slot
	Checks []ClientCheck            `json:"checks,omitempty"`
	// Names the server supplies for each form: dotted link paths
	// (set.workout.sport) and own slots that are not form inputs.
	Given []string `json:"given,omitempty"`
}

type ClientField struct {
	When         any            `json:"when,omitempty"`
	RequiredWhen any            `json:"required_when,omitempty"`
	Options      []ClientOption `json:"options,omitempty"`
}

type ClientOption struct {
	When   any      `json:"when"`
	Values []string `json:"values"`
}

type ClientMatch struct {
	Slot string `json:"slot"` // target slot, carried on each option
	Expr any    `json:"expr"`
}

type ClientCheck struct {
	Expr    any    `json:"expr"`
	Message string `json:"message"`
	Slot    string `json:"slot,omitempty"`
}

// ClientRules returns the browser rules of every class that has any, keyed
// by class name.
func ClientRules(m *model.Model) map[string]*ClientClass {
	out := map[string]*ClientClass{}
	for _, c := range m.ConcreteClasses() {
		if cc := clientClass(c); cc != nil {
			out[c.Name] = cc
		}
	}
	return out
}

// ClientRulesJSON is ClientRules encoded with sorted keys, so the output (and
// any hash of it) is stable for a given schema.
func ClientRulesJSON(m *model.Model) ([]byte, error) { return json.Marshal(ClientRules(m)) }

func clientClass(c *model.Class) *ClientClass {
	cc := &ClientClass{Types: map[string]string{}, Fields: map[string]*ClientField{}, Match: map[string][]ClientMatch{}}
	formSlots := map[string]bool{}
	for _, s := range c.FormSlots() {
		formSlots[s.Name] = true
	}
	given := map[string]bool{}
	read := func(e *expr.Expr) any {
		for _, id := range e.Idents() {
			if s := c.Slot(id); s != nil {
				cc.Types[id] = s.Type
				if !formSlots[id] {
					given[id] = true
				}
			} else {
				given[id] = true // a link path, or the primary key
			}
		}
		return e.Tree()
	}
	field := func(name string) *ClientField {
		if cc.Fields[name] == nil {
			cc.Fields[name] = &ClientField{}
		}
		return cc.Fields[name]
	}
	for _, s := range c.AllSlots() {
		if s.When != nil {
			field(s.Name).When = read(s.When)
		}
		if s.RequiredWhen != nil {
			field(s.Name).RequiredWhen = read(s.RequiredWhen)
		}
		for _, o := range s.Options {
			f := field(s.Name)
			f.Options = append(f.Options, ClientOption{When: read(o.When), Values: o.Values})
		}
	}
	for _, r := range c.AllRelationships() {
		for _, a := range r.Match {
			cc.Match[r.Via] = append(cc.Match[r.Via], ClientMatch{Slot: a.Slot, Expr: read(a.Expr)})
		}
	}
	for _, ck := range c.Checks {
		if ck.Client {
			cc.Checks = append(cc.Checks, ClientCheck{Expr: read(ck.Expr), Message: ck.Message, Slot: ck.Slot})
		}
	}
	if len(cc.Fields) == 0 && len(cc.Match) == 0 && len(cc.Checks) == 0 {
		return nil
	}
	for g := range given {
		cc.Given = append(cc.Given, g)
	}
	sort.Strings(cc.Given)
	return cc
}
