// Package schema reads YAML schema files into a model.Model, validates the
// result, and resolves inheritance and cross-references.
package schema

import (
	"fmt"
	"os"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// Source is one schema document: a file path, or inline YAML via Inline.
type Source struct {
	Path   string
	Inline []byte
}

// File returns a Source for a path on disk.
func File(path string) Source { return Source{Path: path} }

// String returns a Source for inline YAML.
func String(doc string) Source { return Source{Inline: []byte(doc)} }

// Parse reads one or more documents and merges them; later documents override
// earlier ones per class and per enum, as in the Perl framework.
func Parse(sources ...Source) (*model.Model, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("schema: no documents provided")
	}
	m := model.New()
	classDefs := map[string]map[string]any{}
	enumDefs := map[string]map[string]any{}

	for _, src := range sources {
		data, label := src.Inline, "(string)"
		if src.Path != "" {
			b, err := os.ReadFile(src.Path)
			if err != nil {
				return nil, fmt.Errorf("schema: cannot read %s: %w", src.Path, err)
			}
			data, label = b, src.Path
			m.SourceFiles = append(m.SourceFiles, src.Path)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("schema (%s): %w", label, err)
		}
		if doc == nil {
			return nil, fmt.Errorf("schema (%s): document must be a mapping", label)
		}
		if v, ok := doc["schema_version"]; ok {
			m.SchemaVersion = str(v)
		}
		if v, ok := doc["framework_version"]; ok {
			m.FrameworkVersion = str(v)
		}
		if v, ok := doc["description"]; ok {
			m.Description = str(v)
		}
		for name, def := range asMap(doc["enums"]) {
			enumDefs[name] = asMap(def)
		}
		for name, def := range asMap(doc["classes"]) {
			classDefs[name] = asMap(def)
		}
	}

	for _, name := range sortedKeys(enumDefs) {
		e, err := buildEnum(name, enumDefs[name])
		if err != nil {
			return nil, err
		}
		if err := m.AddEnum(e); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedKeys(classDefs) {
		c, err := buildClass(name, classDefs[name])
		if err != nil {
			return nil, err
		}
		if err := m.AddClass(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func buildEnum(name string, def map[string]any) (*model.Enum, error) {
	raw, ok := def["values"].([]any)
	if !ok {
		if _, present := def["values"]; !present {
			return nil, fmt.Errorf("enum %q: missing 'values'", name)
		}
		return nil, fmt.Errorf("enum %q: values must be a list", name)
	}
	values := make([]string, len(raw))
	for i, v := range raw {
		values[i] = str(v)
	}
	return model.NewEnum(name, values, optStr(def, "default"), str(def["description"]))
}

func buildClass(name string, def map[string]any) (*model.Class, error) {
	c := &model.Class{
		Name:        name,
		Label:       str(def["label"]),
		Description: str(def["description"]),
		Table:       str(def["table"]),
		Abstract:    truthy(def["abstract"]),
		IsA:         str(def["is_a"]),
		Display:     str(def["display"]),
		OrderBy:     str(def["order_by"]),
		Calendar:    str(def["calendar"]),
	}

	if slots, ok := def["slots"].([]any); ok {
		for _, sd := range slots {
			s, err := buildSlot(asMap(sd), name)
			if err != nil {
				return nil, err
			}
			if err := c.AddSlot(s); err != nil {
				return nil, err
			}
		}
	}

	rels := asMap(def["relationships"])
	for _, kind := range []string{"belongs_to", "has_many", "many_to_many"} {
		list, _ := rels[kind].([]any)
		for _, rd := range list {
			r, err := buildRelationship(name, kind, asMap(rd))
			if err != nil {
				return nil, err
			}
			c.AddRelationship(r)
		}
	}

	cons := asMap(def["constraints"])
	if uq, ok := cons["unique"].([]any); ok && len(uq) > 0 {
		var groups [][]string
		if _, nested := uq[0].([]any); nested {
			for _, g := range uq {
				groups = append(groups, strList(g))
			}
		} else {
			groups = append(groups, strList(uq))
		}
		for _, cols := range groups {
			k, err := model.NewConstraint(model.Constraint{Kind: "unique", Columns: cols, Owner: name})
			if err != nil {
				return nil, fmt.Errorf("class %q: %w", name, err)
			}
			c.AddConstraint(k)
		}
	}
	if idx, ok := cons["indexes"].([]any); ok {
		for _, id := range idx {
			var cols []string
			var iname string
			if m, isMap := id.(map[string]any); isMap {
				cols, iname = strList(m["columns"]), str(m["name"])
			} else {
				cols = strList(id)
			}
			if len(cols) == 0 {
				return nil, fmt.Errorf("class %q: index missing columns", name)
			}
			k, err := model.NewConstraint(model.Constraint{Kind: "index", Name: iname, Columns: cols, Owner: name})
			if err != nil {
				return nil, fmt.Errorf("class %q: %w", name, err)
			}
			c.AddConstraint(k)
		}
	}

	if a := asMap(def["access"]); len(a) > 0 {
		c.Access = &model.Access{
			Owner:  str(a["owner"]),
			Read:   str(a["read"]),
			Write:  str(a["write"]),
			Parent: str(a["parent"]),
		}
		if pw := asMap(a["public_when"]); len(pw) > 0 {
			c.Access.PublicWhen = map[string]string{}
			for k, v := range pw {
				c.Access.PublicWhen[k] = str(v)
			}
		}
	}
	if a := asMap(def["auth"]); len(a) > 0 {
		c.Auth = &model.Auth{Login: str(a["login"]), Password: str(a["password"]), Display: str(a["display"])}
	}
	return c, nil
}

func buildRelationship(owner, kind string, d map[string]any) (*model.Relationship, error) {
	target := str(d["target"])
	if target == "" {
		return nil, fmt.Errorf("class %q: %s entry missing 'target'", owner, kind)
	}
	if kind == "belongs_to" && str(d["via"]) == "" {
		return nil, fmt.Errorf("class %q: belongs_to entry for %q missing 'via'", owner, target)
	}
	if kind == "many_to_many" && str(d["through"]) == "" {
		return nil, fmt.Errorf("class %q: many_to_many entry for %q missing 'through'", owner, target)
	}
	rname := str(d["name"])
	if rname == "" {
		rname = model.SnakeCase(target)
		if kind != "belongs_to" && !hasSuffixS(rname) {
			rname += "s"
		}
	}
	return model.NewRelationship(model.Relationship{
		Kind:        kind,
		Name:        rname,
		Target:      target,
		Via:         str(d["via"]),
		Through:     str(d["through"]),
		Label:       str(d["label"]),
		Description: str(d["description"]),
		Required:    kind == "belongs_to" && truthy(d["required"]),
		OrderBy:     str(d["order_by"]),
		OnDelete:    str(d["on_delete"]),
		Owner:       owner,
	})
}

func buildSlot(d map[string]any, className string) (*model.Slot, error) {
	name := str(d["name"])
	if name == "" {
		return nil, fmt.Errorf("class %q: slot missing 'name'", className)
	}
	if str(d["type"]) == "" {
		return nil, fmt.Errorf("class %q: slot %q missing 'type'", className, name)
	}
	order := 999
	if v, ok := d["ui_order"]; ok {
		if n, err := strconv.Atoi(str(v)); err == nil {
			order = n
		}
	}
	return model.NewSlot(model.Slot{
		Name:          name,
		Type:          str(d["type"]),
		Enum:          str(d["enum"]),
		Required:      truthy(d["required"]),
		Default:       optStr(d, "default"),
		Autogenerated: truthy(d["autogenerated"]),
		ReadOnly:      truthy(d["read_only"]),
		APIReadOnly:   truthy(d["api_read_only"]),
		Hidden:        truthy(d["hidden"]),
		Label:         str(d["label"]),
		Description:   str(d["description"]),
		Placeholder:   str(d["placeholder"]),
		ControlType:   str(d["control_type"]),
		UIOrder:       order,
		UIGroup:       str(d["ui_group"]),
		Min:           optNum(d, "min"),
		Max:           optNum(d, "max"),
		Pattern:       str(d["pattern"]),
		DefinedIn:     className,
	})
}

// str renders a YAML scalar as a string. Booleans become "1"/"0", matching how
// the Perl framework stores boolean defaults.
func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func optStr(d map[string]any, key string) *string {
	v, ok := d[key]
	if !ok || v == nil {
		return nil
	}
	s := str(v)
	return &s
}

func optNum(d map[string]any, key string) *float64 {
	v, ok := d[key]
	if !ok || v == nil {
		return nil
	}
	f, err := strconv.ParseFloat(str(v), 64)
	if err != nil {
		return nil
	}
	return &f
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case string:
		return x != "" && x != "0" && x != "false"
	}
	return false
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func strList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, str(x))
	}
	return out
}

func hasSuffixS(s string) bool { return len(s) > 0 && s[len(s)-1] == 's' }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
