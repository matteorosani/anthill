package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// schema is the small part of JSON Schema the ant tools need: objects
// with named properties, strings, integers, booleans and arrays. One
// value serves both as the input schema the agent sees and as the check
// of the arguments it sends, so the two cannot drift apart.
type schema struct {
	Type        string
	Description string
	// Properties are an object's properties, in the order shown.
	Properties []property
	// Items is the schema of an array's elements.
	Items *schema
	// Enum lists the allowed values of a string.
	Enum []string
	// Minimum is the smallest allowed integer, or nil for none.
	Minimum *int
}

// property is one property of an object schema.
type property struct {
	Name     string
	Required bool
	Schema   schema
}

// The schema types.
const (
	typeObject  = "object"
	typeString  = "string"
	typeInteger = "integer"
	typeBoolean = "boolean"
	typeArray   = "array"
)

func object(props ...property) schema { return schema{Type: typeObject, Properties: props} }

func required(name string, s schema) property { return property{Name: name, Required: true, Schema: s} }

func optional(name string, s schema) property { return property{Name: name, Schema: s} }

func str(description string, enum ...string) schema {
	return schema{Type: typeString, Description: description, Enum: enum}
}

func integer(description string, minimum int) schema {
	return schema{Type: typeInteger, Description: description, Minimum: &minimum}
}

func boolean(description string) schema { return schema{Type: typeBoolean, Description: description} }

func array(description string, items schema) schema {
	return schema{Type: typeArray, Description: description, Items: &items}
}

// MarshalJSON writes the JSON Schema (draft 2020-12) of s. Objects allow
// no other properties than theirs.
func (s schema) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": s.Type}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if len(s.Enum) > 0 {
		m["enum"] = s.Enum
	}
	if s.Minimum != nil {
		m["minimum"] = *s.Minimum
	}
	if s.Items != nil {
		m["items"] = s.Items
	}
	if s.Type == typeObject {
		props := map[string]schema{}
		req := []string{}
		for _, p := range s.Properties {
			props[p.Name] = p.Schema
			if p.Required {
				req = append(req, p.Name)
			}
		}
		m["properties"] = props
		m["required"] = req
		m["additionalProperties"] = false
	}
	return json.Marshal(m)
}

// validate checks raw JSON arguments against s, an object schema. An
// empty raw counts as an empty object. The error says which argument is
// wrong and why, for the agent.
func (s schema) validate(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return fmt.Errorf("the arguments are not valid JSON: %w", err)
	}
	return s.check("the arguments", v)
}

// check checks the decoded value v, named where for messages.
func (s schema) check(where string, v any) error {
	switch s.Type {
	case typeObject:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", where)
		}
		for name := range obj {
			if !slices.ContainsFunc(s.Properties, func(p property) bool { return p.Name == name }) {
				return fmt.Errorf("%s: unknown argument %q", where, name)
			}
		}
		for _, p := range s.Properties {
			pv, ok := obj[p.Name]
			if !ok || pv == nil {
				if p.Required {
					return fmt.Errorf("%s: %s is required", where, p.Name)
				}
				continue
			}
			if err := p.Schema.check(p.Name, pv); err != nil {
				return err
			}
		}
	case typeString:
		sv, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", where)
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, sv) {
			return fmt.Errorf("%s must be one of %s, not %q", where, strings.Join(s.Enum, ", "), sv)
		}
	case typeInteger:
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be an integer", where)
		}
		i, err := n.Int64()
		if err != nil {
			return fmt.Errorf("%s must be an integer, not %s", where, n)
		}
		if s.Minimum != nil && i < int64(*s.Minimum) {
			return fmt.Errorf("%s must be at least %d, not %d", where, *s.Minimum, i)
		}
	case typeBoolean:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s must be true or false", where)
		}
	case typeArray:
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", where)
		}
		for i, e := range list {
			if err := s.Items.check(fmt.Sprintf("%s[%d]", where, i), e); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", where, s.Type)
	}
	return nil
}
