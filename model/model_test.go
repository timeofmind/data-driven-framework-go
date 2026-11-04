package model

import "testing"

func TestNaming(t *testing.T) {
	for in, want := range map[string]string{
		"Device": "devices", "DeviceClass": "device_classes", "Category": "categories",
		"Address": "addresses", "Day": "days", "TrainingPlan": "training_plans",
	} {
		if got := (&Class{Name: in}).EffectiveTable(); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if got := (&Class{Name: "NetworkDevice"}).EffectiveLabel(); got != "Network Device" {
		t.Errorf("label %q", got)
	}
	if got := (&Slot{Name: "ip_address"}).EffectiveLabel(); got != "Ip Address" {
		t.Errorf("slot label %q", got)
	}
	if got := (&Constraint{Kind: "unique", Columns: []string{"a", "b"}}).EffectiveName("t"); got != "uq_t_a_b" {
		t.Errorf("constraint name %q", got)
	}
}

func TestValueTypes(t *testing.T) {
	for in, want := range map[string]string{
		"INT(11) UNSIGNED": "integer", "tinyint(1)": "boolean", "TINYINT(4)": "integer",
		"varchar(64)": "string", "DECIMAL(10,2)": "float", "datetime": "datetime",
		"timestamp": "datetime", "longtext": "text", "enum('a','b')": "enum", "date": "date",
	} {
		if got := FromMySQLType(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if HTMLInputType("datetime") != "datetime-local" || MySQLType("boolean") != "TINYINT(1)" {
		t.Error("type maps")
	}
	j := JSONSchemaType("date")
	j["x"] = 1
	if _, leaked := JSONSchemaType("date")["x"]; leaked {
		t.Error("JSONSchemaType must return a copy")
	}
}

func TestEnum(t *testing.T) {
	d := "b"
	e, err := NewEnum("E", []string{"a", "b", "it's"}, &d, "")
	if err != nil {
		t.Fatal(err)
	}
	if !e.HasValue("a") || e.HasValue("z") {
		t.Error("HasValue")
	}
	if e.MySQLList() != "'a', 'b', 'it''s'" {
		t.Errorf("list %s", e.MySQLList())
	}
	bad := "z"
	if _, err := NewEnum("E", []string{"a"}, &bad, ""); err == nil {
		t.Error("expected bad default error")
	}
}
