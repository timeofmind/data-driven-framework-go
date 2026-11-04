package schema

import (
	"github.com/timeofmind/data-driven-framework-go/model"
)

// Resolve links parent classes, fills table names, flattens inherited slots,
// attaches enum objects to slots, and adds the inverse has_many for every
// belongs_to that lacks one. Unknown references are left for Validate to report.
func Resolve(m *model.Model) *model.Model {
	for _, c := range m.Classes() {
		if c.HasParent() {
			c.ParentClass = m.Class(c.IsA)
		}
		if c.Table == "" {
			c.Table = c.EffectiveTable()
		}
	}
	for _, c := range m.ClassesInDependencyOrder() {
		resolveSlots(c)
		if p := c.ParentClass; p != nil {
			if c.Access == nil {
				c.Access = p.Access
			}
			if c.Display == "" {
				c.Display = p.Display
			}
			if c.OrderBy == "" {
				c.OrderBy = p.OrderBy
			}
			if c.Calendar == "" {
				c.Calendar = p.Calendar
			}
		}
	}
	for _, c := range m.Classes() {
		for _, s := range c.AllSlots() {
			if s.Type == "enum" && s.Enum != "" {
				s.EnumObj = m.Enum(s.Enum)
			}
		}
	}
	autoHasMany(m)
	return m
}

func resolveSlots(c *model.Class) {
	var resolved []*model.Slot
	if p := c.ParentClass; p != nil {
		resolved = append(resolved, p.AllSlots()...)
	}
	for _, s := range c.Slots {
		kept := resolved[:0:0]
		for _, r := range resolved {
			if r.Name != s.Name {
				kept = append(kept, r)
			}
		}
		resolved = append(kept, s)
	}
	c.ResolvedSlots = resolved
}

func autoHasMany(m *model.Model) {
	for _, c := range m.Classes() {
		for _, r := range c.Relationships {
			if !r.IsBelongsTo() {
				continue
			}
			target := m.Class(r.Target)
			if target == nil {
				continue
			}
			exists := false
			for _, tr := range target.Relationships {
				if tr.IsHasMany() && tr.Target == c.Name && (tr.Via == "" || tr.Via == r.Via) {
					exists = true
					break
				}
			}
			if exists {
				continue
			}
			name := model.SnakeCase(c.Name)
			if !hasSuffixS(name) {
				name += "s"
			}
			if target.Relationship(name) != nil {
				name = name + "_by_" + r.Via
			}
			target.AddRelationship(&model.Relationship{
				Kind:   "has_many",
				Name:   name,
				Target: c.Name,
				Via:    r.Via,
				Label:  c.EffectiveLabel() + "s",
				Owner:  target.Name,
			})
		}
	}
}

// Load parses, resolves and validates the given sources in one step.
func Load(sources ...Source) (*model.Model, error) {
	m, err := Parse(sources...)
	if err != nil {
		return nil, err
	}
	Resolve(m)
	if err := ValidateOrError(m); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadFiles is Load for paths on disk.
func LoadFiles(paths ...string) (*model.Model, error) {
	src := make([]Source, len(paths))
	for i, p := range paths {
		src[i] = File(p)
	}
	return Load(src...)
}
