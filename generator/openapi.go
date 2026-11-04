package generator

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/timeofmind/data-driven-framework-go/model"
)

// OpenAPIOptions configures the OpenAPI generator.
type OpenAPIOptions struct {
	Title    string
	Version  string
	BasePath string // default "/api/v1"
}

// ResourcePath returns the URL segment for a class: its table, lower-case, "_" -> "-".
func ResourcePath(c *model.Class) string {
	return strings.ReplaceAll(strings.ToLower(c.EffectiveTable()), "_", "-")
}

// OpenAPI returns an OpenAPI 3.0.3 document with list/create/get/update/delete
// operations for every concrete class.
func OpenAPI(m *model.Model, o OpenAPIOptions) map[string]any {
	if o.BasePath == "" {
		o.BasePath = "/api/v1"
	}
	schemas := map[string]any{}
	for _, e := range m.Enums() {
		schemas[e.Name] = enumSchema(e)
	}
	paths := map[string]any{}
	for _, c := range m.ConcreteClasses() {
		n := c.Name
		schemas[n] = responseSchema(c)
		schemas[n+"Input"] = inputSchema(c)
		schemas[n+"List"] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"data":     map[string]any{"type": "array", "items": ref(n)},
				"total":    map[string]any{"type": "integer"},
				"page":     map[string]any{"type": "integer"},
				"per_page": map[string]any{"type": "integer"},
			},
		}
		base := o.BasePath + "/" + ResourcePath(c)
		paths[base] = map[string]any{"get": listOp(c), "post": createOp(c)}
		paths[base+"/{id}"] = map[string]any{"get": getOp(c), "put": updateOp(c), "delete": deleteOp(c)}
	}
	schemas["Error"] = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"error":   map[string]any{"type": "string"},
			"message": map[string]any{"type": "string"},
			"code":    map[string]any{"type": "integer"},
			"fields":  map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
		"required": []string{"error", "message"},
	}
	title := firstNonEmpty(o.Title, m.Description, "Generated API")
	version := firstNonEmpty(o.Version, m.SchemaVersion)
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": title, "version": version, "description": m.Description},
		"paths":   paths,
		"components": map[string]any{
			"schemas": schemas,
			"responses": map[string]any{
				"BadRequest": map[string]any{"description": "Bad request", "content": jsonContent(ref("Error"))},
				"NotFound":   map[string]any{"description": "Not found", "content": jsonContent(ref("Error"))},
			},
		},
	}
}

func OpenAPIYAML(m *model.Model, o OpenAPIOptions) ([]byte, error) {
	return yaml.Marshal(OpenAPI(m, o))
}

func OpenAPIJSON(m *model.Model, o OpenAPIOptions) ([]byte, error) {
	return json.MarshalIndent(OpenAPI(m, o), "", "  ")
}

func responseSchema(c *model.Class) map[string]any {
	props := map[string]any{}
	for _, s := range c.AllSlots() {
		if s.Hidden || s.Type == "password" {
			continue
		}
		props[s.Name] = SlotJSONSchema(s)
	}
	out := map[string]any{"type": "object", "properties": props}
	if c.Description != "" {
		out["description"] = c.Description
	}
	return out
}

func inputSchema(c *model.Class) map[string]any {
	props := map[string]any{}
	var required []string
	for _, s := range c.AllSlots() {
		if !s.Writable() || (s.Hidden && !isFK(c, s)) {
			continue
		}
		if c.Access != nil && c.Access.Owner == s.Name {
			continue // filled from the authenticated principal
		}
		props[s.Name] = SlotJSONSchema(s)
		if s.Required {
			required = append(required, s.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func isFK(c *model.Class, s *model.Slot) bool { return c.BelongsToVia(s.Name) != nil }

func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

func jsonContent(schema any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": schema}}
}

func idParam() map[string]any {
	return map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer"}}
}

func listOp(c *model.Class) map[string]any {
	return map[string]any{
		"summary":     "List " + c.EffectiveLabel() + "s",
		"operationId": "list" + c.Name,
		"tags":        []string{c.Name},
		"parameters": []any{
			map[string]any{"name": "page", "in": "query", "schema": map[string]any{"type": "integer", "default": 1}},
			map[string]any{"name": "per_page", "in": "query", "schema": map[string]any{"type": "integer", "default": 20}},
			map[string]any{"name": "order_by", "in": "query", "schema": map[string]any{"type": "string"}},
		},
		"responses": map[string]any{
			"200": map[string]any{"description": "A list of " + c.EffectiveLabel() + "s", "content": jsonContent(ref(c.Name + "List"))},
			"400": map[string]any{"$ref": "#/components/responses/BadRequest"},
		},
	}
}

func createOp(c *model.Class) map[string]any {
	return map[string]any{
		"summary":     "Create a " + c.EffectiveLabel(),
		"operationId": "create" + c.Name,
		"tags":        []string{c.Name},
		"requestBody": map[string]any{"required": true, "content": jsonContent(ref(c.Name + "Input"))},
		"responses": map[string]any{
			"201": map[string]any{"description": "Created", "content": jsonContent(ref(c.Name))},
			"400": map[string]any{"description": "Validation error", "content": jsonContent(ref("Error"))},
			"422": map[string]any{"description": "Unprocessable entity", "content": jsonContent(ref("Error"))},
		},
	}
}

func getOp(c *model.Class) map[string]any {
	return map[string]any{
		"summary":     "Get a " + c.EffectiveLabel() + " by ID",
		"operationId": "get" + c.Name,
		"tags":        []string{c.Name},
		"parameters":  []any{idParam()},
		"responses": map[string]any{
			"200": map[string]any{"description": "Found", "content": jsonContent(ref(c.Name))},
			"404": map[string]any{"$ref": "#/components/responses/NotFound"},
		},
	}
}

func updateOp(c *model.Class) map[string]any {
	return map[string]any{
		"summary":     "Update a " + c.EffectiveLabel(),
		"operationId": "update" + c.Name,
		"tags":        []string{c.Name},
		"parameters":  []any{idParam()},
		"requestBody": map[string]any{"required": true, "content": jsonContent(ref(c.Name + "Input"))},
		"responses": map[string]any{
			"200": map[string]any{"description": "Updated", "content": jsonContent(ref(c.Name))},
			"400": map[string]any{"description": "Validation error", "content": jsonContent(ref("Error"))},
			"404": map[string]any{"$ref": "#/components/responses/NotFound"},
		},
	}
}

func deleteOp(c *model.Class) map[string]any {
	return map[string]any{
		"summary":     "Delete a " + c.EffectiveLabel(),
		"operationId": "delete" + c.Name,
		"tags":        []string{c.Name},
		"parameters":  []any{idParam()},
		"responses": map[string]any{
			"204": map[string]any{"description": "Deleted"},
			"404": map[string]any{"$ref": "#/components/responses/NotFound"},
		},
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
