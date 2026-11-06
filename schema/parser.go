// Package schema reads YAML schema files into a model.Model, validates the
// result, and resolves inheritance and cross-references.
package schema

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/timeofmind/data-driven-framework-go/expr"

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
	e, err := model.NewEnum(name, values, optStr(def, "default"), str(def["description"]))
	if err != nil {
		return nil, err
	}
	if lm := asMap(def["labels"]); len(lm) > 0 {
		e.Labels = map[string]string{}
		for k, v := range lm {
			if !e.HasValue(k) {
				return nil, fmt.Errorf("enum %q: label for unknown value %q", name, k)
			}
			e.Labels[k] = str(v)
		}
	}
	return e, nil
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
	if v, ok := def["search"]; ok && !truthy(v) {
		c.NoSearch = true
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

	// A sequence is unique within its parent.
	for _, s := range c.Slots {
		if s.Sequence != "" {
			k, err := model.NewConstraint(model.Constraint{Kind: "unique", Columns: []string{s.Sequence, s.Name}, Owner: name})
			if err != nil {
				return nil, fmt.Errorf("class %q: %w", name, err)
			}
			c.AddConstraint(k)
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
	if err := buildBehaviour(c, def); err != nil {
		return nil, fmt.Errorf("class %q: %w", name, err)
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
	var rank *model.Rank
	switch rv := d["rank"].(type) {
	case bool:
		if rv {
			rank = &model.Rank{}
		}
	case map[string]any:
		rank = &model.Rank{BestPer: str(rv["best_per"])}
	}
	addWhen, err := optExpr(d, "add_when")
	if err != nil {
		return nil, fmt.Errorf("class %q: relationship to %q: %w", owner, target, err)
	}
	match, err := assigns("match", d["match"])
	if err != nil {
		return nil, fmt.Errorf("class %q: relationship to %q: %w", owner, target, err)
	}
	return model.NewRelationship(model.Relationship{
		Match:       match,
		Rank:        rank,
		AddLabel:    str(d["add_label"]),
		AddWhen:     addWhen,
		Link:        str(d["link"]),
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
	when, err := optExpr(d, "when")
	if err != nil {
		return nil, fmt.Errorf("class %q: slot %q: %w", className, name, err)
	}
	reqWhen, err := optExpr(d, "required_when")
	if err != nil {
		return nil, fmt.Errorf("class %q: slot %q: %w", className, name, err)
	}
	var options []*model.OptionRule
	if list, ok := d["options"].([]any); ok {
		for _, item := range list {
			od := asMap(item)
			w, err := parseExpr("options when", od["when"])
			if err != nil {
				return nil, fmt.Errorf("class %q: slot %q: %w", className, name, err)
			}
			options = append(options, &model.OptionRule{When: w, Values: strList(od["values"])})
		}
	} else if _, present := d["options"]; present {
		return nil, fmt.Errorf("class %q: slot %q: options must be a list of {when, values}", className, name)
	}
	seq := str(d["sequence"])
	if m := asMap(d["sequence"]); len(m) > 0 {
		seq = str(m["within"])
	}
	if _, present := d["sequence"]; present && seq == "" {
		return nil, fmt.Errorf("class %q: slot %q: sequence needs within: <parent FK slot>", className, name)
	}
	readOnly := truthy(d["read_only"]) || seq != "" // a sequence is assigned, never entered
	return model.NewSlot(model.Slot{
		Sequence:      seq,
		When:          when,
		RequiredWhen:  reqWhen,
		Options:       options,
		Name:          name,
		Type:          str(d["type"]),
		Enum:          str(d["enum"]),
		Required:      truthy(d["required"]),
		Default:       optStr(d, "default"),
		Autogenerated: truthy(d["autogenerated"]),
		ReadOnly:      readOnly,
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

func optExpr(d map[string]any, key string) (*expr.Expr, error) {
	v, ok := d[key]
	if !ok || v == nil {
		return nil, nil
	}
	return parseExpr(key, v)
}

func parseExpr(what string, v any) (*expr.Expr, error) {
	src := str(v)
	if src == "" {
		return nil, fmt.Errorf("%s: empty expression", what)
	}
	e, err := expr.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return e, nil
}

// assigns parses a {slot: expression} mapping in sorted key order.
func assigns(what string, v any) ([]model.Assign, error) {
	m := asMap(v)
	var out []model.Assign
	for _, k := range sortedKeys(m) {
		e, err := parseExpr(what+"."+k, m[k])
		if err != nil {
			return nil, err
		}
		out = append(out, model.Assign{Slot: k, Expr: e})
	}
	return out, nil
}

func buildBehaviour(c *model.Class, def map[string]any) error {
	var err error
	if c.CalendarLabel, err = optExpr(def, "calendar_label"); err != nil {
		return err
	}
	if c.Title, err = optExpr(def, "title"); err != nil {
		return err
	}
	if list, ok := def["checks"].([]any); ok {
		for _, item := range list {
			d := asMap(item)
			ck := &model.Check{Message: str(d["message"]), Slot: str(d["slot"]), Client: truthy(d["client"])}
			if ck.Expr, err = parseExpr("check", d["check"]); err != nil {
				return err
			}
			if ck.Message == "" {
				return fmt.Errorf("check %q: missing message", ck.Expr)
			}
			c.Checks = append(c.Checks, ck)
		}
	} else if _, present := def["checks"]; present {
		return fmt.Errorf("checks must be a list of {check, message, slot}")
	}
	if list, ok := def["computed"].([]any); ok {
		for _, item := range list {
			d := asMap(item)
			cp := &model.Computed{Name: str(d["name"]), Label: str(d["label"]), Format: str(d["format"]), Suffix: str(d["suffix"])}
			if cp.Name == "" {
				return fmt.Errorf("computed entry missing name")
			}
			if n, err := strconv.Atoi(str(d["decimals"])); err == nil {
				cp.Decimals = n
			}
			if cp.Expr, err = parseExpr("computed "+cp.Name, d["expr"]); err != nil {
				return err
			}
			if cp.When, err = optExpr(d, "when"); err != nil {
				return fmt.Errorf("computed %s: %w", cp.Name, err)
			}
			c.Computed = append(c.Computed, cp)
		}
	}
	if list, ok := def["triggers"].([]any); ok {
		for _, item := range list {
			d := asMap(item)
			tr := &model.Trigger{Via: str(d["via"])}
			if tr.If, err = optExpr(d, "if"); err != nil {
				return fmt.Errorf("trigger via %s: %w", tr.Via, err)
			}
			if tr.Set, err = assigns("trigger set", d["set"]); err != nil {
				return err
			}
			c.Triggers = append(c.Triggers, tr)
		}
	}
	if d := asMap(def["import"]); len(d) > 0 {
		im := &model.Import{Formats: strList(d["formats"]), Dedupe: str(d["dedupe"])}
		if im.Timezone, err = optExpr(d, "timezone"); err != nil {
			return err
		}
		if im.Fields, err = assigns("import fields", d["fields"]); err != nil {
			return err
		}
		if fb := asMap(d["fallback"]); len(fb) > 0 {
			im.Fallback = map[string]string{}
			for k, v := range fb {
				im.Fallback[k] = str(v)
			}
		}
		for _, item := range asList(d["children"]) {
			cd := asMap(item)
			ch := model.ImportChild{Relationship: str(cd["relationship"]), From: str(cd["from"])}
			ch.Min, _ = strconv.Atoi(str(cd["min"]))
			if ch.Fields, err = assigns("import children fields", cd["fields"]); err != nil {
				return err
			}
			im.Children = append(im.Children, ch)
		}
		for _, item := range asList(d["link"]) {
			ld := asMap(item)
			ln := model.ImportLink{Via: str(ld["via"])}
			if ln.Where, err = assigns("import link where", ld["where"]); err != nil {
				return err
			}
			if ln.If, err = optExpr(ld, "if"); err != nil {
				return err
			}
			if ln.Prefer, err = optExpr(ld, "prefer"); err != nil {
				return err
			}
			im.Links = append(im.Links, ln)
		}
		for _, item := range asList(d["create"]) {
			cd := asMap(item)
			from := asMap(cd["from"])
			cr := model.ImportCreate{Class: str(cd["class"]), FromClass: str(from["class"]), FromFollow: str(from["follow"]), Unique: strList(cd["unique"])}
			if cr.FromWhere, err = assigns("import create from.where", from["where"]); err != nil {
				return err
			}
			if cr.If, err = optExpr(cd, "if"); err != nil {
				return err
			}
			if cr.Set, err = assigns("import create set", cd["set"]); err != nil {
				return err
			}
			im.Creates = append(im.Creates, cr)
		}
		c.Import = im
	}
	for _, item := range asList(def["filters"]) {
		d := asMap(item)
		f := &model.Filter{
			Name: str(d["name"]), Label: str(d["label"]), Via: str(d["via"]), Slot: str(d["slot"]),
			Match: str(d["match"]), IncludeChildren: str(d["include_children"]), GroupBy: str(d["group_by"]),
			RankByUsage: truthy(d["rank_by_usage"]), CollapseGroups: truthy(d["collapse_groups"]),
		}
		if f.Name == "" {
			f.Name = strings.TrimSuffix(f.Slot, "_id")
		}
		if f.Match == "" {
			f.Match = "all"
		}
		c.Filters = append(c.Filters, f)
	}
	for _, item := range asList(def["actions"]) {
		d := asMap(item)
		a := &model.Action{Name: str(d["name"]), Label: str(d["label"]), Copy: strList(d["copy"])}
		if a.Name == "" {
			return fmt.Errorf("action entry missing name")
		}
		if a.Set, err = assigns("action "+a.Name+" set", d["set"]); err != nil {
			return err
		}
		if a.When, err = optExpr(d, "when"); err != nil {
			return fmt.Errorf("action %s: %w", a.Name, err)
		}
		c.Actions = append(c.Actions, a)
	}
	if d := asMap(def["usage"]); len(d) > 0 {
		u := &model.Usage{Log: str(d["log"]), Via: str(d["via"]), Kind: str(d["kind"]), Filter: str(d["filter"]), Values: str(d["values"])}
		for _, item := range asList(d["uses"]) {
			ud := asMap(item)
			use := model.UsageUse{Kind: str(ud["kind"]), Action: str(ud["action"]), Record: str(ud["record"])}
			if l := str(ud["link"]); l != "" {
				cls, via, ok := strings.Cut(l, ".")
				if !ok {
					return fmt.Errorf("usage use %q: link must be Class.fk_slot", use.Kind)
				}
				use.LinkClass, use.LinkVia = cls, via
			}
			u.Uses = append(u.Uses, use)
		}
		c.Usage = u
	}
	return nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
