package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var compiled atomic.Int64

// Compile parses b as a JSON Schema (draft 2020-12 unless it says otherwise)
// and returns a validator. Format is asserted, not just annotated: a contract
// that says "date-time" means it.
func Compile(b []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := fmt.Sprintf("mem://contract/%d.json", compiled.Add(1))
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("not a valid JSON Schema: %w", err)
	}
	return s, nil
}

// Validate reports whether b is a usable contract: valid JSON Schema whose root
// describes an object, since a dataset contract describes records.
func Validate(b []byte) error {
	if _, err := Compile(b); err != nil {
		return err
	}
	v, err := decode(b)
	if err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("the contract must be a JSON object schema")
	}
	if t, ok := m["type"]; !ok || t != "object" {
		return fmt.Errorf(`the contract root must declare "type": "object"`)
	}
	return nil
}

// Canonical re-encodes a schema with sorted keys so that versions diff cleanly.
func Canonical(b []byte) ([]byte, error) {
	v, err := decode(b)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}
