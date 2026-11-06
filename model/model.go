// Package model is the canonical in-memory representation of a schema: classes,
// slots, enums, relationships and constraints. It is independent of YAML and of
// every output target, and is the input to every generator and to the runtime.
package model

import (
	"crypto/md5"

	"encoding/hex"
	"fmt"
	"github.com/timeofmind/data-driven-framework-go/expr"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Model holds every class and enum in a schema.
type Model struct {
	SchemaVersion    string
	FrameworkVersion string
	Description      string
	SourceFiles      []string

	classes map[string]*Class
	enums   map[string]*Enum
}

func New() *Model {
	return &Model{
		SchemaVersion:    "1.0.0",
		FrameworkVersion: "0.01",
		classes:          map[string]*Class{},
		enums:            map[string]*Enum{},
	}
}

func (m *Model) AddClass(c *Class) error {
	if _, dup := m.classes[c.Name]; dup {
		return fmt.Errorf("duplicate class %q", c.Name)
	}
	m.classes[c.Name] = c
	return nil
}

func (m *Model) AddEnum(e *Enum) error {
	if _, dup := m.enums[e.Name]; dup {
		return fmt.Errorf("duplicate enum %q", e.Name)
	}
	m.enums[e.Name] = e
	return nil
}

func (m *Model) Class(name string) *Class { return m.classes[name] }
func (m *Model) Enum(name string) *Enum   { return m.enums[name] }
func (m *Model) HasClass(name string) bool {
	_, ok := m.classes[name]
	return ok
}
func (m *Model) HasEnum(name string) bool {
	_, ok := m.enums[name]
	return ok
}

func (m *Model) ClassNames() []string { return sortedKeys(m.classes) }
func (m *Model) EnumNames() []string  { return sortedKeys(m.enums) }

// Classes returns all classes sorted by name.
func (m *Model) Classes() []*Class {
	out := make([]*Class, 0, len(m.classes))
	for _, n := range m.ClassNames() {
		out = append(out, m.classes[n])
	}
	return out
}

// Enums returns all enums sorted by name.
func (m *Model) Enums() []*Enum {
	out := make([]*Enum, 0, len(m.enums))
	for _, n := range m.EnumNames() {
		out = append(out, m.enums[n])
	}
	return out
}

// ClassByTable finds the class owning a table name.
func (m *Model) ClassByTable(table string) *Class {
	for _, c := range m.classes {
		if c.EffectiveTable() == table {
			return c
		}
	}
	return nil
}

// ClassesInDependencyOrder sorts classes so that parents (is_a) and belongs_to
// targets appear before the classes that reference them. The Perl original only
// ordered by is_a; ordering FK targets first lets SQLite DDL run top to bottom.
func (m *Model) ClassesInDependencyOrder() []*Class {
	visited := map[string]bool{}
	var ordered []*Class
	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		c := m.classes[name]
		if c == nil {
			return
		}
		if c.IsA != "" {
			visit(c.IsA)
		}
		for _, r := range c.Relationships {
			if r.IsBelongsTo() && r.Target != name {
				visit(r.Target)
			}
		}
		ordered = append(ordered, c)
	}
	for _, n := range m.ClassNames() {
		visit(n)
	}
	return ordered
}

// ConcreteClasses returns non-abstract classes in dependency order.
func (m *Model) ConcreteClasses() []*Class {
	var out []*Class
	for _, c := range m.ClassesInDependencyOrder() {
		if !c.Abstract {
			out = append(out, c)
		}
	}
	return out
}

// Hash is a stable MD5 digest of the model's structure, for change detection.
// It matches the Perl framework's model_hash for the same schema.
func (m *Model) Hash() string {
	parts := []string{"schema_version=" + m.SchemaVersion}
	for _, n := range m.ClassNames() {
		c := m.classes[n]
		parts = append(parts, "class="+n, "table="+c.EffectiveTable())
		for _, s := range c.Slots {
			parts = append(parts, "slot="+s.Name+"/"+s.Type)
		}
	}
	for _, n := range m.EnumNames() {
		parts = append(parts, "enum="+n+":"+strings.Join(m.enums[n].Values, ","))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// Enum is a named list of allowed string values.
type Enum struct {
	Name        string
	Values      []string
	Default     *string
	Description string
	Labels      map[string]string // optional display text per value
}

// Label returns the display text for an enum value: its declared label, else
// the value with underscores as spaces and the first letter capitalised.
func (e *Enum) Label(v string) string {
	if l, ok := e.Labels[v]; ok && l != "" {
		return l
	}
	s := strings.ReplaceAll(v, "_", " ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func NewEnum(name string, values []string, def *string, desc string) (*Enum, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("enum %q: values must be a non-empty list", name)
	}
	e := &Enum{Name: name, Values: values, Default: def, Description: desc}
	if def != nil && !e.HasValue(*def) {
		return nil, fmt.Errorf("enum %q: default %q is not in values list", name, *def)
	}
	return e, nil
}

func (e *Enum) HasValue(v string) bool {
	for _, x := range e.Values {
		if x == v {
			return true
		}
	}
	return false
}

// MySQLList returns the quoted, comma-separated list for ENUM(...).
func (e *Enum) MySQLList() string {
	q := make([]string, len(e.Values))
	for i, v := range e.Values {
		q[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(q, ", ")
}

// Slot is one field of a class.
type Slot struct {
	Name          string
	Type          string
	Enum          string
	Required      bool
	Default       *string
	Autogenerated bool
	ReadOnly      bool
	APIReadOnly   bool
	Hidden        bool

	Label       string
	Description string
	Placeholder string
	ControlType string
	UIOrder     int
	UIGroup     string

	Min     *float64
	Max     *float64
	Pattern string

	// Rules (Go-port extensions). When: the slot applies only while this holds
	// for the record — otherwise its value is cleared on save and it is not
	// shown. RequiredWhen: required while this holds. Options: for enum slots,
	// the first rule whose when holds limits the allowed values.
	When         *expr.Expr
	RequiredWhen *expr.Expr
	Options      []*OptionRule
	// Sequence: an integer numbering rows within a parent (the FK slot named
	// here), e.g. set # within a workout. Assigned on save — each new row gets
	// the number after its parent's highest, as does a row moved to another
	// parent — and unique per parent. Clients cannot set it.
	Sequence string
	// DefaultExpr: the value a new record starts with, computed from its other
	// values and its links (e.g. weight for a resistance workout's reps,
	// distance otherwise). Applied where a static default would be.
	DefaultExpr *expr.Expr

	EnumObj   *Enum  // set by the resolver
	DefinedIn string // class the slot was declared in

	patternRE *regexp.Regexp
}

func NewSlot(s Slot) (*Slot, error) {
	if !IsValidType(s.Type) {
		return nil, fmt.Errorf("slot %q: unknown type %q", s.Name, s.Type)
	}
	if s.Type == "enum" && s.Enum == "" {
		return nil, fmt.Errorf("slot %q: type=enum requires 'enum' attribute", s.Name)
	}
	return &s, nil
}

func (s *Slot) EffectiveLabel() string {
	if s.Label != "" {
		return s.Label
	}
	words := strings.Split(s.Name, "_")
	for i, w := range words {
		if w != "" {
			r := []rune(w)
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
	}
	return strings.Join(words, " ")
}

func (s *Slot) EffectiveControlType() string {
	if s.ControlType != "" {
		return s.ControlType
	}
	return HTMLInputType(s.Type)
}

func (s *Slot) IsNullable() bool { return !s.Required && !s.Autogenerated }

// Writable reports whether clients may set this slot via forms or the API.
func (s *Slot) Writable() bool {
	return !s.Autogenerated && !s.ReadOnly && !s.APIReadOnly
}

// PatternRE returns the compiled pattern, or nil when there is none.
func (s *Slot) PatternRE() *regexp.Regexp {
	if s.Pattern == "" {
		return nil
	}
	if s.patternRE == nil {
		s.patternRE, _ = regexp.Compile(s.Pattern)
	}
	return s.patternRE
}

// Relationship links two classes.
type Relationship struct {
	Kind        string // belongs_to | has_many | many_to_many
	Name        string
	Target      string
	Via         string
	Through     string
	Label       string
	Description string
	Required    bool
	OrderBy     string // has_many: "slot [asc|desc]" for listing children
	OnDelete    string // belongs_to: cascade | set_null | restrict (Go-port extension)
	Owner       string

	// has_many presentation (Go-port extensions)
	Rank     *Rank      // leaderboard numbering, optionally best row per slot
	AddLabel string     // label for the "add" action, e.g. "Register"
	AddWhen  *expr.Expr // the add action is offered only when this holds for the parent row

	// belongs_to: which target rows may be linked. "own" (the default for targets
	// with an owner): only the linker's own rows; "readable": any row they can read.
	Link string

	// belongs_to: Match limits linkable target rows to those whose slot equals
	// the expression evaluated on the linking record, e.g. {sport: sport}.
	Match []Assign
}

func NewRelationship(r Relationship) (*Relationship, error) {
	switch r.Kind {
	case "belongs_to", "has_many", "many_to_many":
	default:
		return nil, fmt.Errorf("relationship: unknown kind %q", r.Kind)
	}
	if r.Kind == "belongs_to" && r.Via == "" {
		return nil, fmt.Errorf("relationship kind=belongs_to requires 'via' (FK field name)")
	}
	if r.Kind == "many_to_many" && r.Through == "" {
		return nil, fmt.Errorf("relationship kind=many_to_many requires 'through' (join class name)")
	}
	switch r.OnDelete {
	case "", "cascade", "set_null", "restrict":
	default:
		return nil, fmt.Errorf("relationship %q: on_delete must be cascade, set_null or restrict", r.Name)
	}
	switch r.Link {
	case "", "own", "readable":
	default:
		return nil, fmt.Errorf("relationship %q: link must be own or readable", r.Name)
	}
	if r.Link != "" && r.Kind != "belongs_to" {
		return nil, fmt.Errorf("relationship %q: link applies to belongs_to only", r.Name)
	}
	return &r, nil
}

func (r *Relationship) IsBelongsTo() bool  { return r.Kind == "belongs_to" }
func (r *Relationship) IsHasMany() bool    { return r.Kind == "has_many" }
func (r *Relationship) IsManyToMany() bool { return r.Kind == "many_to_many" }

func (r *Relationship) EffectiveLabel() string {
	if r.Label != "" {
		return r.Label
	}
	return SplitCamel(r.Target)
}

// Constraint is a unique key or index.
type Constraint struct {
	Kind    string // unique | index
	Name    string
	Columns []string
	Owner   string
}

func NewConstraint(c Constraint) (*Constraint, error) {
	if c.Kind != "unique" && c.Kind != "index" {
		return nil, fmt.Errorf("constraint: unknown kind %q", c.Kind)
	}
	if len(c.Columns) == 0 {
		return nil, fmt.Errorf("constraint: columns must be a non-empty list")
	}
	return &c, nil
}

func (c *Constraint) IsUnique() bool { return c.Kind == "unique" }
func (c *Constraint) IsIndex() bool  { return c.Kind == "index" }

func (c *Constraint) EffectiveName(table string) string {
	if c.Name != "" {
		return c.Name
	}
	prefix := "idx"
	if c.IsUnique() {
		prefix = "uq"
	}
	return strings.Join(append([]string{prefix, table}, c.Columns...), "_")
}

// Access is a Go-port extension: row ownership and read/write rules, enforced by
// the runtime's access layer and by web front ends such as the Caddy module.
//
//	access:
//	  owner: user_id              # slot filled from the signed-in principal
//	  read: public                # public | authenticated | owner
//	  write: owner                # owner | authenticated | nobody
//	  public_when: {visibility: public}   # rows readable by anyone when all match
//	  parent: activity_id         # belongs_to FK: rows are readable only when the
//	                              # parent row is; with no owner slot, writes follow it too
type Access struct {
	Owner      string
	Read       string
	Write      string
	PublicWhen map[string]string
	Parent     string
}

// Auth is a Go-port extension marking the class whose rows are login principals.
type Auth struct {
	Login    string // slot holding the login identifier (e.g. email)
	Password string // slot of type password
	Display  string // slot shown as the principal's name
}

// Class is one entity in the schema.
type Class struct {
	Name          string
	Label         string
	Description   string
	Table         string
	Abstract      bool
	IsA           string
	Slots         []*Slot
	Relationships []*Relationship
	Constraints   []*Constraint

	// Go-port extensions (all optional).
	Display  string // slot used as the record's title in links and selectors
	OrderBy  string // default list ordering, "slot [asc|desc]"
	Calendar string // date slot, or a path to one on a linked record (competition.start_date); front ends may render a calendar view
	// CalendarEnd: the last day a record covers (same forms as Calendar), so a
	// multi-day record shows on every day from Calendar to CalendarEnd.
	CalendarEnd string
	// NotPersonal (personal_calendar: false): the class has its own calendar
	// view but its rows do not appear on their owner's personal calendar —
	// e.g. a shared listing someone added, as opposed to their entry in it.
	NotPersonal bool
	Access      *Access
	Auth        *Auth

	Computed      []*Computed
	CalendarLabel *expr.Expr // text for calendar entries; falls back to the display slot
	Triggers      []*Trigger
	Import        *Import
	Filters       []*Filter
	Actions       []*Action
	Usage         *Usage
	NoSearch      bool       // search: false — lists offer no free-text search (e.g. filter-only libraries)
	Title         *expr.Expr // record title in links, pickers and headings; falls back to the display slot
	OnChange      []*OnChange
	Checks        []*Check // rules a record must satisfy to be saved

	ParentClass   *Class  // set by the resolver
	ResolvedSlots []*Slot // own + inherited, set by the resolver
}

func (c *Class) EffectiveLabel() string {
	if c.Label != "" {
		return c.Label
	}
	return SplitCamel(c.Name)
}

// EffectiveTable returns the explicit table or the snake_case plural of the name.
func (c *Class) EffectiveTable() string {
	if c.Table != "" {
		return c.Table
	}
	return Pluralize(SnakeCase(c.Name))
}

func (c *Class) Slot(name string) *Slot {
	for _, s := range c.AllSlots() {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (c *Class) OwnSlot(name string) *Slot {
	for _, s := range c.Slots {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (c *Class) HasSlot(name string) bool { return c.Slot(name) != nil }

// AllRelationships returns own relationships followed by inherited ones.
func (c *Class) AllRelationships() []*Relationship {
	out := append([]*Relationship(nil), c.Relationships...)
	if c.ParentClass != nil && c.ParentClass != c {
		for _, r := range c.ParentClass.AllRelationships() {
			if c.ownRelationship(r.Name) == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

func (c *Class) ownRelationship(name string) *Relationship {
	for _, r := range c.Relationships {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Relationship finds a relationship by name, including inherited ones.
func (c *Class) Relationship(name string) *Relationship {
	for _, r := range c.AllRelationships() {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// BelongsToVia returns the belongs_to relationship using the given FK slot.
func (c *Class) BelongsToVia(slot string) *Relationship {
	for _, r := range c.AllRelationships() {
		if r.IsBelongsTo() && r.Via == slot {
			return r
		}
	}
	return nil
}

func (c *Class) AddSlot(s *Slot) error {
	if c.OwnSlot(s.Name) != nil {
		return fmt.Errorf("class %q: duplicate slot %q", c.Name, s.Name)
	}
	c.Slots = append(c.Slots, s)
	return nil
}

func (c *Class) AddRelationship(r *Relationship) { c.Relationships = append(c.Relationships, r) }
func (c *Class) AddConstraint(k *Constraint)     { c.Constraints = append(c.Constraints, k) }

// AllSlots returns own and inherited slots once resolved, else own slots.
func (c *Class) AllSlots() []*Slot {
	if c.ResolvedSlots != nil {
		return c.ResolvedSlots
	}
	return c.Slots
}

// PKSlot returns the autogenerated integer primary key, or nil.
func (c *Class) PKSlot() *Slot {
	for _, s := range c.AllSlots() {
		if s.Autogenerated && s.Type == "integer" {
			return s
		}
	}
	return nil
}

func (c *Class) UniqueConstraints() []*Constraint { return c.constraintsOf("unique") }
func (c *Class) IndexConstraints() []*Constraint  { return c.constraintsOf("index") }

func (c *Class) constraintsOf(kind string) []*Constraint {
	var out []*Constraint
	for _, k := range c.Constraints {
		if k.Kind == kind {
			out = append(out, k)
		}
	}
	return out
}

func (c *Class) HasParent() bool { return c.IsA != "" }

// IsJoinedChild reports whether rows live in a parent table plus this table
// (joined-table inheritance from a concrete parent).
func (c *Class) IsJoinedChild() bool {
	return c.HasParent() && c.ParentClass != nil && !c.ParentClass.Abstract
}

// DisplaySlot returns the slot used as a record title: the explicit display
// slot, else the first of name/title/label, else the first visible string slot.
func (c *Class) DisplaySlot() *Slot {
	if c.Display != "" {
		if s := c.Slot(c.Display); s != nil {
			return s
		}
	}
	for _, n := range []string{"name", "title", "label", "display_name"} {
		if s := c.Slot(n); s != nil {
			return s
		}
	}
	for _, s := range c.AllSlots() {
		if s.Type == "string" && !s.Hidden {
			return s
		}
	}
	return c.PKSlot()
}

// VisibleSlots returns non-hidden slots ordered by ui_order (stable).
func (c *Class) VisibleSlots() []*Slot {
	var out []*Slot
	for _, s := range c.AllSlots() {
		if !s.Hidden && s.Type != "password" {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UIOrder < out[j].UIOrder })
	return out
}

// FormSlots returns slots shown in create/edit forms, ordered by ui_order.
func (c *Class) FormSlots() []*Slot {
	var out []*Slot
	for _, s := range c.AllSlots() {
		if !s.Hidden && !s.ReadOnly && !s.Autogenerated {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UIOrder < out[j].UIOrder })
	return out
}

// SnakeCase converts PascalCase to snake_case: DeviceClass -> device_class.
func SnakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Pluralize applies the framework's simple English pluralisation.
func Pluralize(name string) string {
	switch {
	case strings.HasSuffix(name, "s"):
		return name + "es"
	case len(name) >= 2 && strings.HasSuffix(name, "y") && !strings.ContainsRune("aeiou", rune(name[len(name)-2])):
		return name[:len(name)-1] + "ies"
	}
	return name + "s"
}

// SplitCamel turns "NetworkDevice" into "Network Device".
func SplitCamel(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CalendarIsPath reports whether the calendar date lives on a linked record.
func (c *Class) CalendarIsPath() bool {
	return strings.Contains(c.Calendar, ".") || strings.Contains(c.CalendarEnd, ".")
}
