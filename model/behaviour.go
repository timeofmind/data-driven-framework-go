package model

import (
	"fmt"
	"strings"

	"github.com/timeofmind/data-driven-framework-go/expr"
)

// The declarative behaviour a schema can attach to classes and relationships
// (Go-port extensions, v0.2). Every expression is parsed when the schema loads.

// Computed is a read-only value derived from a record's slots and shown with
// it (e.g. pace from time and distance).
//
//	computed:
//	  - name: pace
//	    label: Pace
//	    expr: "coalesce(moving_time, duration) / distance_km"
//	    format: duration          # duration | number | text
//	    decimals: 1               # for number
//	    suffix: " /km"
//	    when: "sport != 'ride'"   # optional condition
type Computed struct {
	Name     string
	Label    string
	Expr     *expr.Expr
	Format   string
	Decimals int
	Suffix   string
	When     *expr.Expr
}

// Trigger updates the record a belongs_to points at whenever this record is
// saved (e.g. mark the planned workout completed when an activity links to it).
// Expressions see this record as self.<slot> and the linked one as target.<slot>.
//
//	triggers:
//	  - via: planned_workout_id
//	    if: "target.status == 'planned'"
//	    set:
//	      status: "if(self.distance_km < 0.8 * target.target_distance_km, 'partial', 'completed')"
type Trigger struct {
	Via string
	If  *expr.Expr
	Set []Assign
}

// Assign sets Slot to the value of Expr.
type Assign struct {
	Slot string
	Expr *expr.Expr
}

// Rank turns a has_many into a leaderboard: children are listed in the
// relationship's order_by, numbered, and (with best_per) reduced to the first
// row per value of that slot — e.g. each athlete's best time.
type Rank struct {
	BestPer string
}

// Import declares that records of a class can be created from uploaded files
// (activity files from watches, timing-system exports, …). Field expressions
// read the parsed file's fields by name and owner.<slot> of the uploader.
//
//	import:
//	  formats: [fit, gpx, tcx, csv]
//	  dedupe: source_hash           # slot; an upload whose value already exists for the owner is skipped
//	  timezone: "owner.timezone"    # where local dates/times are computed
//	  fields: {date: date_local, title: title, distance_km: distance_km, ...}
//	  fallback: {sport: other}      # used when a value is not in the slot's enum
//	  children:
//	    - {relationship: laps, from: laps, min: 2, fields: {lap_number: index, ...}}
//	  link:
//	    - via: planned_workout_id
//	      where: {athlete_id: self.athlete_id, date: self.date}
//	      if: "item.sport == self.sport"
//	      prefer: "item.status == 'planned'"
//	  create:
//	    - class: EventResult
//	      from: {class: EventRegistration, where: {athlete_id: self.athlete_id}, follow: event_id}
//	      if: "item.start_date <= self.date and item.end_date >= self.date"
//	      unique: [event_id, activity_id]
//	      set: {event_id: item.id, activity_id: self.id, elapsed_time: "coalesce(self.moving_time, self.duration)"}
type Import struct {
	Formats  []string
	Dedupe   string
	Timezone *expr.Expr
	Fields   []Assign
	Fallback map[string]string
	Children []ImportChild
	Links    []ImportLink
	Creates  []ImportCreate
}

// ImportChild creates has_many children from a list in the parsed file.
type ImportChild struct {
	Relationship string
	From         string
	Min          int
	Fields       []Assign
}

// ImportLink sets a belongs_to FK on the new record to a matching row.
type ImportLink struct {
	Via    string
	Where  []Assign // target slot = expression over self.*
	If     *expr.Expr
	Prefer *expr.Expr
}

// ImportCreate creates records in another class for each matching row.
type ImportCreate struct {
	Class      string
	FromClass  string
	FromWhere  []Assign
	FromFollow string // optional belongs_to FK on FromClass to iterate its targets instead
	If         *expr.Expr
	Unique     []string
	Set        []Assign
}

// Filter lets lists of a class be narrowed by values of another class reached
// through a has_many link table (e.g. workouts by tag, through WorkoutTag).
//
//	filters:
//	  - name: tag                     # URL parameter (?tag=3&tag=7); default: slot without _id
//	    label: Tags
//	    via: tags                     # has_many relationship on this class (to the link class)
//	    slot: tag_id                  # FK on the link class holding the value
//	    match: all                    # all (default): every chosen value; any: at least one
//	    include_children: parent_id   # FK on the value class: a value also matches its descendants
//	    group_by: category_id         # FK on the value class to group the choices by
//	    collapse_groups: true         # show groups closed until opened (or holding a choice)
//	    rank_by_usage: true           # order results by the class's usage log for these values
type Filter struct {
	Name            string
	Label           string
	Via             string
	Slot            string
	Match           string
	IncludeChildren string
	GroupBy         string
	CollapseGroups  bool
	RankByUsage     bool
}

// Action is an operation offered on a record besides edit and delete. The one
// kind so far is copy: a new record owned by the person acting, with the
// source's values, the listed has_many children copied (paths may nest:
// "sets.intervals"), and Set applied (expressions see the original as source.*).
//
//	actions:
//	  - name: copy
//	    label: "Copy to my workouts"
//	    copy: [sets, sets.intervals, tags]
//	    set: {cloned_from_id: source.id, visibility: "'private'"}
//	    when: "visibility == 'public'"   # optional, on the source record
type Action struct {
	Name  string
	Label string
	Copy  []string
	Set   []Assign
	When  *expr.Expr
	// SetChildren applies to each record copied along a copy path (keyed by
	// the path, e.g. workouts). Expressions read the child being copied as
	// source.* and the record the action runs on as root.*.
	SetChildren map[string][]Assign
	// CopyLinks copies records that copied children link to, with the linked
	// class's own action, and points the child at the copy: keyed by
	// "path.fk_slot" (e.g. workouts.workout_id), valued by the action name.
	// A record linked from several children is copied once. The linked
	// action's when-condition is not applied: running this action is what
	// shares the linked records.
	CopyLinks map[string]string
}

// OnChange runs when a saved record's Slot changes: Set updates the record
// itself, Children updates every row of each named has_many. Expressions read
// the new values bare (or as self.*), the stored values as old.*, and — for
// children — the child as child.*.
//
//	on_change:
//	  - slot: start_date
//	    set: {end_date: "add_days(old.end_date, days_between(old.start_date, start_date))"}
//	    children:
//	      workouts: {date: "add_days(child.date, days_between(old.start_date, start_date))"}
type OnChange struct {
	Slot     string
	Set      []Assign
	Children map[string][]Assign
}

// Usage records each time someone uses a record of this class — links a new
// record to it, or runs one of its actions — together with the filter values
// they had chosen to find it, so lists can rank records by how often they were
// chosen under the same filters.
//
//	usage:
//	  log: WorkoutSelection           # class that gets a row per use (its owner slot = who)
//	  via: workout_id                 # FK on the log class to the used record
//	  kind: action                    # enum slot on the log class naming the use
//	  filter: tag                     # filter whose chosen values are recorded (optional)
//	  values: search_tags             # has_many on the log class holding those values
//	  uses:
//	    - {kind: scheduled, link: PlannedWorkout.workout_id, record: planned_workout_id}
//	    - {kind: cloned, action: copy, record: clone_id}
type Usage struct {
	Log    string
	Via    string
	Kind   string
	Filter string
	Values string
	Uses   []UsageUse
}

// UsageUse is one way of using a record: a new LinkClass row whose LinkVia FK
// points at it, or running Action. Record is the log's FK to the new row.
type UsageUse struct {
	Kind      string
	LinkClass string
	LinkVia   string
	Action    string
	Record    string
}

// FilterByName returns the class's filter with that name, or nil.
func (c *Class) FilterByName(name string) *Filter {
	for _, f := range c.Filters {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// ActionByName returns the class's action with that name, or nil.
func (c *Class) ActionByName(name string) *Action {
	for _, a := range c.Actions {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// RelationshipByName returns the class's relationship (own, inherited or
// inverse) with that name, or nil.
func (c *Class) RelationshipByName(name string) *Relationship {
	for _, r := range c.AllRelationships() {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// FilterValueClass is the class whose rows are a filter's values (e.g. Tag).
func (m *Model) FilterValueClass(c *Class, f *Filter) *Class {
	rel := c.RelationshipByName(f.Via)
	if rel == nil {
		return nil
	}
	link := m.Class(rel.Target)
	if link == nil {
		return nil
	}
	br := link.BelongsToVia(f.Slot)
	if br == nil {
		return nil
	}
	return m.Class(br.Target)
}

// UsesByLink returns the classes whose usage counts a new row of class via
// the FK slot (e.g. PlannedWorkout.workout_id counts as using a Workout).
func (m *Model) UsesByLink(class, slot string) []UsageTarget {
	var out []UsageTarget
	for _, c := range m.ConcreteClasses() {
		if c.Usage == nil {
			continue
		}
		for i := range c.Usage.Uses {
			if u := &c.Usage.Uses[i]; u.LinkClass == class && u.LinkVia == slot {
				out = append(out, UsageTarget{Class: c, Use: u})
			}
		}
	}
	return out
}

// UseByAction returns the usage entry for running a class's action, or nil.
func (c *Class) UseByAction(action string) *UsageUse {
	if c.Usage == nil {
		return nil
	}
	for i := range c.Usage.Uses {
		if c.Usage.Uses[i].Action == action {
			return &c.Usage.Uses[i]
		}
	}
	return nil
}

// UsageTarget pairs a class that logs usage with one of its uses.
type UsageTarget struct {
	Class *Class
	Use   *UsageUse
}

// BelongsToTarget returns the class's first belongs_to pointing at target, or nil.
func (c *Class) BelongsToTarget(target string) *Relationship {
	for _, r := range c.AllRelationships() {
		if r.IsBelongsTo() && r.Target == target {
			return r
		}
	}
	return nil
}

// OptionRule limits an enum slot's allowed values while When holds.
type OptionRule struct {
	When   *expr.Expr
	Values []string
}

// Check is a class rule: Expr must hold for a record to be saved. Slot, if
// set, is the field the message is shown against. Client checks (own slots
// only) are also run in the browser as the form is filled in.
type Check struct {
	Expr    *expr.Expr
	Message string
	Slot    string
	Client  bool
}

// LinkStep is one belongs_to hop in a path such as set_id.workout_id.sport.
type LinkStep struct {
	Rel   *Relationship
	Class *Class // the class reached
}

// ResolvePath splits a dotted identifier into belongs_to hops and a final
// slot of the class reached. A hop may name the FK slot (workout_id), the
// relationship, or the FK without its _id suffix (workout).
func ResolvePath(m *Model, c *Class, path string) ([]LinkStep, error) {
	parts := strings.Split(path, ".")
	var steps []LinkStep
	cur := c
	for i, p := range parts {
		if i == len(parts)-1 {
			if !cur.HasSlot(p) && !(p == "id" && cur.PKSlot() != nil) {
				return nil, fmt.Errorf("%s has no slot %q", cur.Name, p)
			}
			return steps, nil
		}
		r := cur.BelongsToVia(p)
		if r == nil {
			r = cur.BelongsToVia(p + "_id")
		}
		if r == nil {
			if x := cur.Relationship(p); x != nil && x.IsBelongsTo() {
				r = x
			}
		}
		if r == nil {
			return nil, fmt.Errorf("%s has no belongs_to link %q", cur.Name, p)
		}
		next := m.Class(r.Target)
		if next == nil {
			return nil, fmt.Errorf("unknown class %q", r.Target)
		}
		steps = append(steps, LinkStep{Rel: r, Class: next})
		cur = next
	}
	return steps, nil
}

// PathSlot returns the slot a path (own slot or link path) ends at, or nil.
func PathSlot(m *Model, c *Class, path string) *Slot {
	steps, err := ResolvePath(m, c, path)
	if err != nil {
		return nil
	}
	cur := c
	if len(steps) > 0 {
		cur = steps[len(steps)-1].Class
	}
	return cur.Slot(path[strings.LastIndexByte(path, '.')+1:])
}
