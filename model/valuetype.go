package model

import (
	"regexp"
	"strings"
)

// PrimitiveTypes are the slot types a schema may use. Go-port extensions:
// "password" is stored as a one-way hash, accepted on input, never emitted;
// "duration" is stored as whole seconds and entered/shown as h:mm:ss.
var PrimitiveTypes = []string{
	"string", "integer", "float", "boolean", "date", "datetime", "text", "enum", "password", "duration",
}

var mysqlTypeMap = map[string]string{
	"string":   "VARCHAR(255)",
	"integer":  "INT",
	"float":    "DOUBLE",
	"boolean":  "TINYINT(1)",
	"date":     "DATE",
	"datetime": "DATETIME",
	"text":     "TEXT",
	"enum":     "ENUM",
	"password": "VARCHAR(255)",
	"duration": "INT",
}

var sqliteTypeMap = map[string]string{
	"string":   "TEXT",
	"integer":  "INTEGER",
	"float":    "REAL",
	"boolean":  "INTEGER",
	"date":     "TEXT",
	"datetime": "TEXT",
	"text":     "TEXT",
	"enum":     "TEXT",
	"password": "TEXT",
	"duration": "INTEGER",
}

var jsonSchemaTypeMap = map[string]map[string]any{
	"string":   {"type": "string"},
	"integer":  {"type": "integer"},
	"float":    {"type": "number"},
	"boolean":  {"type": "boolean"},
	"date":     {"type": "string", "format": "date"},
	"datetime": {"type": "string", "format": "date-time"},
	"text":     {"type": "string"},
	"enum":     {"type": "string"},
	"password": {"type": "string", "format": "password", "writeOnly": true},
	"duration": {"type": "integer", "description": "duration in seconds"},
}

var goTypeMap = map[string]string{
	"string":   "string",
	"integer":  "int64",
	"float":    "float64",
	"boolean":  "bool",
	"date":     "string",
	"datetime": "string",
	"text":     "string",
	"enum":     "string",
	"password": "string",
	"duration": "int64",
}

var htmlInputMap = map[string]string{
	"string":   "text",
	"integer":  "number",
	"float":    "number",
	"boolean":  "checkbox",
	"date":     "date",
	"datetime": "datetime-local",
	"text":     "textarea",
	"enum":     "select",
	"password": "password",
	"duration": "duration",
}

// IsValidType reports whether t is a known primitive type.
func IsValidType(t string) bool {
	for _, p := range PrimitiveTypes {
		if p == t {
			return true
		}
	}
	return false
}

func MySQLType(t string) string {
	if v, ok := mysqlTypeMap[t]; ok {
		return v
	}
	return "VARCHAR(255)"
}

func SQLiteType(t string) string {
	if v, ok := sqliteTypeMap[t]; ok {
		return v
	}
	return "TEXT"
}

// JSONSchemaType returns a fresh map, safe for the caller to modify.
func JSONSchemaType(t string) map[string]any {
	src, ok := jsonSchemaTypeMap[t]
	if !ok {
		src = jsonSchemaTypeMap["string"]
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func GoType(t string) string {
	if v, ok := goTypeMap[t]; ok {
		return v
	}
	return "string"
}

func HTMLInputType(t string) string {
	if v, ok := htmlInputMap[t]; ok {
		return v
	}
	return "text"
}

var (
	sizeSuffix   = regexp.MustCompile(`\(.*\)`)
	unsignedWord = regexp.MustCompile(`\s+UNSIGNED`)
)

// FromMySQLType maps a MySQL column type back to a framework type (reverse generator).
// TINYINT(1) is reported as boolean; every other integer width as integer.
func FromMySQLType(columnType string) string {
	ct := strings.ToUpper(strings.TrimSpace(columnType))
	if strings.HasPrefix(ct, "TINYINT(1)") {
		return "boolean"
	}
	ct = sizeSuffix.ReplaceAllString(ct, "")
	ct = strings.TrimSpace(unsignedWord.ReplaceAllString(ct, ""))
	switch ct {
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "MEDIUMINT":
		return "integer"
	case "FLOAT", "DOUBLE", "DECIMAL", "NUMERIC", "REAL":
		return "float"
	case "BOOLEAN", "BOOL":
		return "boolean"
	case "DATE":
		return "date"
	case "DATETIME", "TIMESTAMP":
		return "datetime"
	case "TEXT", "MEDIUMTEXT", "LONGTEXT", "TINYTEXT", "BLOB", "MEDIUMBLOB", "LONGBLOB":
		return "text"
	case "ENUM":
		return "enum"
	}
	return "string"
}
