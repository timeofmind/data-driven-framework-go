package model

import "github.com/timeofmind/data-driven-framework-go/expr"

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
