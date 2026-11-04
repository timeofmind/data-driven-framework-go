package expr

import "testing"

func env(vals map[string]any) Env {
	return func(n string) (any, bool) {
		v, ok := vals[n]
		return v, ok
	}
}

func TestEval(t *testing.T) {
	vals := map[string]any{
		"duration": int64(2650), "moving_time": int64(0), "distance_km": 10.02, "sport": "run",
		"self.date": "2026-11-03", "item.start_date": "2026-11-01", "item.end_date": nil, "title": "Evening Run",
		"target.target_distance_km": nil, "none": nil, "open": true,
	}
	cases := map[string]any{
		"coalesce(moving_time, duration) / distance_km":                                          2650 / 10.02,
		"round(distance_km, 1)":                                                                  10.0,
		"duration(coalesce(moving_time, duration) / distance_km)":                                "4:24",
		"title + ' · ' + round(distance_km, 2) + ' km'":                                          "Evening Run · 10.02 km",
		"title + ' · ' + none + ' km'":                                                           nil,
		"item.start_date <= self.date and (item.end_date == null or item.end_date >= self.date)": true,
		"if(distance_km < 0.8 * target.target_distance_km, 'partial', 'completed')":              "completed",
		"sport != 'ride' and sport != 'swim'":                                                    true,
		"not open":                                                                               false,
		"distance_km / 0":                                                                        nil,
		"-distance_km + 1":                                                                       1 - 10.02,
		"max(1, 5, null, 3)":                                                                     5.0,
		"lower('ABC') == 'abc' && open":                                                          true,
	}
	for src, want := range cases {
		e, err := Parse(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		got, err := e.Eval(env(vals))
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", src, got, err, want)
		}
	}
}

func TestErrors(t *testing.T) {
	for _, src := range []string{"1 +", "(1", "'open", "nope(1)", "if(1, 2)", "a $ b"} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q parsed", src)
		}
	}
	e := MustParse("missing + 1")
	if _, err := e.Eval(env(nil)); err == nil {
		t.Error("unknown identifier should error")
	}
	if got := MustParse("a + b.c * coalesce(d, a)").Idents(); len(got) != 3 || got[1] != "b.c" {
		t.Errorf("idents %v", got)
	}
}
