// Package expr is the small expression language schemas use to declare
// behaviour: computed fields, trigger conditions and values, import mappings.
//
//	coalesce(moving_time, duration) / distance_km
//	if(self.distance_km < 0.8 * target.target_distance_km, 'partial', 'completed')
//	item.start_date <= self.date and (item.end_date == null or item.end_date >= self.date)
//	title + ' · ' + round(distance_km, 1) + ' km'
//	add_days(source.date, days_between(root.start_date, input.start_date))
//
// Values are nil, float64, string or bool. Identifiers may be dotted
// (self.date, item.sport); the caller's Env resolves them. Arithmetic on null
// gives null, comparisons with null are false (except == / != null), division
// by zero gives null, and + concatenates when either side is a string (null
// still gives null, so a label falls back cleanly when a value is missing).
// Expressions have no side effects and no access to anything but Env.
package expr

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Env resolves identifiers. ok=false means the name is unknown (an error);
// a known name with no value returns (nil, true).
type Env func(name string) (value any, ok bool)

// Expr is a parsed expression.
type Expr struct {
	src  string
	root node
}

func (e *Expr) String() string { return e.src }

// Idents returns every identifier the expression reads, in order of appearance.
func (e *Expr) Idents() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n node)
	walk = func(n node) {
		switch x := n.(type) {
		case ident:
			if !seen[string(x)] {
				seen[string(x)] = true
				out = append(out, string(x))
			}
		case unary:
			walk(x.x)
		case binary:
			walk(x.l)
			walk(x.r)
		case call:
			for _, a := range x.args {
				walk(a)
			}
		}
	}
	walk(e.root)
	return out
}

// Parse compiles src.
func Parse(src string) (*Expr, error) {
	p := &parser{src: src}
	if err := p.lex(); err != nil {
		return nil, err
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, fmt.Errorf("expr %q: unexpected %q", src, p.toks[p.pos].s)
	}
	return &Expr{src: src, root: n}, nil
}

// MustParse is Parse that panics; for expressions fixed at compile time.
func MustParse(src string) *Expr {
	e, err := Parse(src)
	if err != nil {
		panic(err)
	}
	return e
}

// Eval evaluates the expression.
func (e *Expr) Eval(env Env) (any, error) { return e.root.eval(env) }

// Truthy reports whether a value counts as true: non-null, non-zero, non-empty, not false.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != "" && x != "0"
	}
	return true
}

// Normalize converts Go values from records into expression values.
func Normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case float32:
		return float64(x)
	case []byte:
		return string(x)
	}
	return v
}

// ---- AST ----

type node interface {
	eval(Env) (any, error)
}

type lit struct{ v any }

func (n lit) eval(Env) (any, error) { return n.v, nil }

type ident string

func (n ident) eval(env Env) (any, error) {
	v, ok := env(string(n))
	if !ok {
		return nil, fmt.Errorf("unknown name %q", string(n))
	}
	return Normalize(v), nil
}

type unary struct {
	op string
	x  node
}

func (n unary) eval(env Env) (any, error) {
	v, err := n.x.eval(env)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "not":
		return !Truthy(v), nil
	case "-":
		if f, ok := v.(float64); ok {
			return -f, nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("bad unary %s", n.op)
}

type binary struct {
	op   string
	l, r node
}

func (n binary) eval(env Env) (any, error) {
	l, err := n.l.eval(env)
	if err != nil {
		return nil, err
	}
	// Short-circuit logic.
	switch n.op {
	case "and":
		if !Truthy(l) {
			return false, nil
		}
		r, err := n.r.eval(env)
		return Truthy(r), err
	case "or":
		if Truthy(l) {
			return true, nil
		}
		r, err := n.r.eval(env)
		return Truthy(r), err
	}
	r, err := n.r.eval(env)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "==", "!=":
		eq := equal(l, r)
		if n.op == "==" {
			return eq, nil
		}
		return !eq, nil
	case "<", "<=", ">", ">=":
		c, ok := compare(l, r)
		if !ok {
			return false, nil
		}
		switch n.op {
		case "<":
			return c < 0, nil
		case "<=":
			return c <= 0, nil
		case ">":
			return c > 0, nil
		}
		return c >= 0, nil
	case "+":
		if l == nil || r == nil {
			return nil, nil
		}
		ls, lStr := l.(string)
		rs, rStr := r.(string)
		if lStr || rStr {
			if !lStr {
				ls = Format(l)
			}
			if !rStr {
				rs = Format(r)
			}
			return ls + rs, nil
		}
		fallthrough
	case "-", "*", "/":
		lf, lok := l.(float64)
		rf, rok := r.(float64)
		if !lok || !rok {
			return nil, nil
		}
		switch n.op {
		case "+":
			return lf + rf, nil
		case "-":
			return lf - rf, nil
		case "*":
			return lf * rf, nil
		}
		if rf == 0 {
			return nil, nil
		}
		return lf / rf, nil
	}
	return nil, fmt.Errorf("bad operator %s", n.op)
}

func equal(l, r any) bool {
	if l == nil || r == nil {
		return l == nil && r == nil
	}
	if lb, ok := l.(bool); ok {
		return lb == Truthy(r)
	}
	if rb, ok := r.(bool); ok {
		return rb == Truthy(l)
	}
	if c, ok := compare(l, r); ok {
		return c == 0
	}
	return false
}

// compare orders two non-null values of the same kind (numbers numerically,
// strings lexically — so ISO dates compare correctly). Numeric strings
// compare as numbers against numbers.
func compare(l, r any) (int, bool) {
	if l == nil || r == nil {
		return 0, false
	}
	lf, lnum := toNum(l)
	rf, rnum := toNum(r)
	if lnum && rnum {
		switch {
		case lf < rf:
			return -1, true
		case lf > rf:
			return 1, true
		}
		return 0, true
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		return strings.Compare(ls, rs), true
	}
	return 0, false
}

func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

type call struct {
	fn   string
	args []node
}

var arity = map[string][2]int{
	"coalesce": {1, -1}, "if": {3, 3}, "round": {1, 2}, "duration": {1, 1},
	"lower": {1, 1}, "upper": {1, 1}, "min": {2, -1}, "max": {2, -1}, "abs": {1, 1},
	"add_days": {2, 2}, "days_between": {2, 2},
}

func (n call) eval(env Env) (any, error) {
	if n.fn == "if" {
		c, err := n.args[0].eval(env)
		if err != nil {
			return nil, err
		}
		if Truthy(c) {
			return n.args[1].eval(env)
		}
		return n.args[2].eval(env)
	}
	args := make([]any, len(n.args))
	for i, a := range n.args {
		v, err := a.eval(env)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	switch n.fn {
	case "coalesce":
		for _, a := range args {
			if a != nil && a != "" {
				if f, ok := a.(float64); ok && f == 0 {
					continue // zero means "not recorded" for measurements
				}
				return a, nil
			}
		}
		return nil, nil
	case "round":
		f, ok := args[0].(float64)
		if !ok {
			return nil, nil
		}
		places := 0.0
		if len(args) == 2 {
			places, _ = args[1].(float64)
		}
		p := math.Pow(10, places)
		return math.Round(f*p) / p, nil
	case "duration":
		f, ok := args[0].(float64)
		if !ok {
			return nil, nil
		}
		return FormatDuration(int64(math.Round(f))), nil
	case "lower", "upper":
		s, ok := args[0].(string)
		if !ok {
			return nil, nil
		}
		if n.fn == "lower" {
			return strings.ToLower(s), nil
		}
		return strings.ToUpper(s), nil
	case "abs":
		f, ok := args[0].(float64)
		if !ok {
			return nil, nil
		}
		return math.Abs(f), nil
	case "add_days":
		d, ok := toDate(args[0])
		n, nok := args[1].(float64)
		if !ok || !nok {
			return nil, nil
		}
		return d.AddDate(0, 0, int(math.Round(n))).Format(dateLayout), nil
	case "days_between":
		a, aok := toDate(args[0])
		b, bok := toDate(args[1])
		if !aok || !bok {
			return nil, nil
		}
		return math.Round(b.Sub(a).Hours() / 24), nil
	case "min", "max":
		var best any
		for _, a := range args {
			if a == nil {
				continue
			}
			if best == nil {
				best = a
				continue
			}
			if c, ok := compare(a, best); ok && ((n.fn == "min" && c < 0) || (n.fn == "max" && c > 0)) {
				best = a
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("unknown function %s", n.fn)
}

// Format renders a value as text (numbers without trailing zeros).
func Format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// FormatDuration renders seconds as h:mm:ss, or m:ss under an hour.
func FormatDuration(sec int64) string {
	neg := ""
	if sec < 0 {
		neg, sec = "-", -sec
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%s%d:%02d:%02d", neg, h, m, s)
	}
	return fmt.Sprintf("%s%d:%02d", neg, m, s)
}

// ---- lexer / parser ----

type tok struct {
	kind byte // n=number s=string i=ident o=operator
	s    string
}

type parser struct {
	src  string
	toks []tok
	pos  int
}

func (p *parser) lex() error {
	rs := []rune(p.src)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case unicode.IsDigit(r) || (r == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			p.toks = append(p.toks, tok{'n', string(rs[i:j])})
			i = j
		case r == '\'' || r == '"':
			j := i + 1
			var b strings.Builder
			for ; j < len(rs) && rs[j] != r; j++ {
				if rs[j] == '\\' && j+1 < len(rs) {
					j++
				}
				b.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return fmt.Errorf("expr %q: unterminated string", p.src)
			}
			p.toks = append(p.toks, tok{'s', b.String()})
			i = j + 1
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' || rs[j] == '.') {
				j++
			}
			p.toks = append(p.toks, tok{'i', string(rs[i:j])})
			i = j
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			switch two {
			case "==", "!=", "<=", ">=", "&&", "||":
				op := map[string]string{"&&": "and", "||": "or"}[two]
				if op == "" {
					op = two
				}
				p.toks = append(p.toks, tok{'o', op})
				i += 2
				continue
			}
			if strings.ContainsRune("+-*/<>(),!", r) {
				op := string(r)
				if op == "!" {
					op = "not"
				}
				p.toks = append(p.toks, tok{'o', op})
				i++
				continue
			}
			return fmt.Errorf("expr %q: unexpected character %q", p.src, r)
		}
	}
	return nil
}

func (p *parser) peek() tok {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return tok{}
}

func (p *parser) accept(kind byte, s string) bool {
	t := p.peek()
	if t.kind == kind && t.s == s {
		p.pos++
		return true
	}
	if kind == 'o' && t.kind == 'i' && (s == "and" || s == "or" || s == "not") && strings.EqualFold(t.s, s) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) parseOr() (node, error) {
	l, err := p.parseAnd()
	for err == nil && p.accept('o', "or") {
		var r node
		r, err = p.parseAnd()
		l = binary{"or", l, r}
	}
	return l, err
}

func (p *parser) parseAnd() (node, error) {
	l, err := p.parseNot()
	for err == nil && p.accept('o', "and") {
		var r node
		r, err = p.parseNot()
		l = binary{"and", l, r}
	}
	return l, err
}

func (p *parser) parseNot() (node, error) {
	if p.accept('o', "not") {
		x, err := p.parseNot()
		return unary{"not", x}, err
	}
	return p.parseCmp()
}

func (p *parser) parseCmp() (node, error) {
	l, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.accept('o', op) {
			r, err := p.parseAdd()
			return binary{op, l, r}, err
		}
	}
	return l, nil
}

func (p *parser) parseAdd() (node, error) {
	l, err := p.parseMul()
	for err == nil {
		var op string
		switch {
		case p.accept('o', "+"):
			op = "+"
		case p.accept('o', "-"):
			op = "-"
		default:
			return l, nil
		}
		var r node
		r, err = p.parseMul()
		l = binary{op, l, r}
	}
	return l, err
}

func (p *parser) parseMul() (node, error) {
	l, err := p.parseUnary()
	for err == nil {
		var op string
		switch {
		case p.accept('o', "*"):
			op = "*"
		case p.accept('o', "/"):
			op = "/"
		default:
			return l, nil
		}
		var r node
		r, err = p.parseUnary()
		l = binary{op, l, r}
	}
	return l, err
}

func (p *parser) parseUnary() (node, error) {
	if p.accept('o', "-") {
		x, err := p.parseUnary()
		return unary{"-", x}, err
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	t := p.peek()
	switch t.kind {
	case 'n':
		p.pos++
		f, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, fmt.Errorf("expr %q: bad number %q", p.src, t.s)
		}
		return lit{f}, nil
	case 's':
		p.pos++
		return lit{t.s}, nil
	case 'i':
		p.pos++
		switch strings.ToLower(t.s) {
		case "null":
			return lit{nil}, nil
		case "true":
			return lit{true}, nil
		case "false":
			return lit{false}, nil
		}
		if p.accept('o', "(") {
			fn := strings.ToLower(t.s)
			ar, ok := arity[fn]
			if !ok {
				return nil, fmt.Errorf("expr %q: unknown function %s()", p.src, t.s)
			}
			var args []node
			if !p.accept('o', ")") {
				for {
					a, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if p.accept('o', ")") {
						break
					}
					if !p.accept('o', ",") {
						return nil, fmt.Errorf("expr %q: expected , or ) in %s()", p.src, fn)
					}
				}
			}
			if len(args) < ar[0] || (ar[1] >= 0 && len(args) > ar[1]) {
				return nil, fmt.Errorf("expr %q: wrong number of arguments to %s()", p.src, fn)
			}
			return call{fn, args}, nil
		}
		return ident(t.s), nil
	case 'o':
		if p.accept('o', "(") {
			n, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.accept('o', ")") {
				return nil, fmt.Errorf("expr %q: missing )", p.src)
			}
			return n, nil
		}
	}
	if t.kind == 0 {
		return nil, fmt.Errorf("expr %q: unexpected end", p.src)
	}
	return nil, fmt.Errorf("expr %q: unexpected %q", p.src, t.s)
}

const dateLayout = "2006-01-02"

// toDate reads a date value: a string starting YYYY-MM-DD (a datetime's
// time of day is ignored).
func toDate(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || len(s) < 10 {
		return time.Time{}, false
	}
	t, err := time.Parse(dateLayout, s[:10])
	return t, err == nil
}
