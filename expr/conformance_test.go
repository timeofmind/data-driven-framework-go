package expr

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestConformance(t *testing.T) {
	var cases []struct {
		Expr string         `json:"expr"`
		Env  map[string]any `json:"env"`
		Want any            `json:"want"`
	}
	if err := json.Unmarshal(Conformance, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		e, err := Parse(c.Expr)
		if err != nil {
			t.Errorf("%s: %v", c.Expr, err)
			continue
		}
		got, err := e.Eval(func(n string) (any, bool) { v, ok := c.Env[n]; return v, ok })
		if err != nil && c.Want != nil {
			t.Errorf("%s: %v", c.Expr, err)
			continue
		}
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("%s with %v = %#v, want %#v", c.Expr, c.Env, got, c.Want)
		}
		// The tree must round-trip through JSON (it is shipped to browsers).
		if _, err := json.Marshal(e.Tree()); err != nil {
			t.Errorf("%s: tree: %v", c.Expr, err)
		}
	}
}
