package app

import (
	"encoding/json"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestSchemaJSON(t *testing.T) {
	s := object(
		required("minutes", integer("Minutes.", 1)),
		optional("where", str("Where.", "item", "pr")),
		optional("points", array("Points.", str("One point."))),
	)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"additionalProperties":false,"properties":{"minutes":{"description":"Minutes.","minimum":1,"type":"integer"},` +
		`"points":{"description":"Points.","items":{"description":"One point.","type":"string"},"type":"array"},` +
		`"where":{"description":"Where.","enum":["item","pr"],"type":"string"}},"required":["minutes"],"type":"object"}`
	if string(data) != want {
		t.Errorf("schema =\n%s\nwant\n%s", data, want)
	}
}

func TestSchemaValidate(t *testing.T) {
	s := object(
		required("minutes", integer("", 1)),
		optional("resolve", boolean("")),
		optional("where", str("", "item", "pr")),
		optional("items", array("", object(required("name", str(""))))),
	)
	for _, tt := range []struct {
		args string
		want string
	}{
		{`{"minutes":5}`, ""},
		{`{"minutes":5,"resolve":true,"where":"pr","items":[{"name":"a"}]}`, ""},
		{`{"minutes":5,"where":null}`, ""},
		{``, "the arguments: minutes is required"},
		{`[]`, "the arguments must be an object"},
		{`{"minutes":"5"}`, "minutes must be an integer"},
		{`{"minutes":1.5}`, "minutes must be an integer, not 1.5"},
		{`{"minutes":0}`, "minutes must be at least 1, not 0"},
		{`{"minutes":5,"resolve":"yes"}`, "resolve must be true or false"},
		{`{"minutes":5,"where":"wiki"}`, `where must be one of item, pr, not "wiki"`},
		{`{"minutes":5,"items":{}}`, "items must be an array"},
		{`{"minutes":5,"items":[{}]}`, "items[0]: name is required"},
		{`{"minutes":5,"extra":1}`, `the arguments: unknown argument "extra"`},
		{`{"minutes":`, "the arguments are not valid JSON: unexpected EOF"},
	} {
		err := s.validate(json.RawMessage(tt.args))
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tt.want {
			t.Errorf("validate(%s) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

// Every tool's schema is an object schema the agent can read.
func TestCatalogueSchemas(t *testing.T) {
	for name, spec := range toolCatalogue {
		if spec.input.Type != typeObject {
			t.Errorf("%s: input is %s, want object", name, spec.input.Type)
		}
		if _, err := json.Marshal(spec.input); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if spec.description == "" {
			t.Errorf("%s has no description", name)
		}
	}
}

// The catalogue has exactly the domain's tools.
func TestCatalogueMatchesTools(t *testing.T) {
	if len(toolCatalogue) != len(domain.AntTools) {
		t.Errorf("catalogue has %d tools, the domain %d", len(toolCatalogue), len(domain.AntTools))
	}
	for _, name := range domain.AntTools {
		if _, ok := toolCatalogue[name]; !ok {
			t.Errorf("%s is not in the catalogue", name)
		}
	}
}
