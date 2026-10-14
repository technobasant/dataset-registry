// Package compat decides whether a new version of a dataset's JSON Schema
// contract is compatible with the published one, and says why, change by change.
//
// Compatibility is defined on validity sets. With V(s) the set of documents a
// schema accepts:
//
//	BACKWARD  V(old) ⊆ V(new)  everything written under the old contract still passes the new one
//	FORWARD   V(new) ⊆ V(old)  everything written under the new contract still passes the old one
//	FULL      both
//
// FORWARD is the one that protects consumers: they built against the old
// contract and will receive data written under the new one.
//
// The checker is structural and conservative. Keywords it cannot reason about
// (anyOf, $ref, patternProperties, multipleOf, …) are reported as breaking in
// both directions whenever they differ, and regular expressions are never
// compared for containment. It may call a safe change breaking; the labelled
// cases in cases.json measure how often, and the tests require that it never
// calls a breaking change safe.
package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Mode is a dataset's compatibility requirement.
type Mode string

// Supported modes.
const (
	Backward Mode = "BACKWARD"
	Forward  Mode = "FORWARD"
	Full     Mode = "FULL"
	None     Mode = "NONE"
)

// ParseMode accepts a mode name in any case.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToUpper(strings.TrimSpace(s)))
	switch m {
	case Backward, Forward, Full, None:
		return m, nil
	}
	return "", fmt.Errorf("unknown compatibility mode %q (want BACKWARD, FORWARD, FULL or NONE)", s)
}

func (m Mode) checksBackward() bool { return m == Backward || m == Full }
func (m Mode) checksForward() bool  { return m == Forward || m == Full }

// Change is one difference between two schema versions.
type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	// BreaksBackward: some document valid under the old schema fails the new one.
	BreaksBackward bool `json:"breaks_backward"`
	// BreaksForward: some document valid under the new schema fails the old one.
	BreaksForward bool `json:"breaks_forward"`
	// Breaking is the verdict under the report's mode.
	Breaking bool   `json:"breaking"`
	Reason   string `json:"reason"`
}

// Report is the outcome of comparing two schema versions under a mode.
type Report struct {
	Mode           Mode     `json:"mode"`
	Compatible     bool     `json:"compatible"`
	BreakingCount  int      `json:"breaking_count"`
	BreaksBackward bool     `json:"breaks_backward"`
	BreaksForward  bool     `json:"breaks_forward"`
	Changes        []Change `json:"changes"`
}

// Compare checks newSchema against oldSchema. Both must be JSON; validating
// that they are well-formed JSON Schema is the caller's job (see Validate).
func Compare(oldSchema, newSchema []byte, mode Mode) (Report, error) {
	o, err := decode(oldSchema)
	if err != nil {
		return Report{}, fmt.Errorf("old schema: %w", err)
	}
	n, err := decode(newSchema)
	if err != nil {
		return Report{}, fmt.Errorf("new schema: %w", err)
	}
	return CompareValues(o, n, mode), nil
}

// CompareValues is Compare over already-decoded schemas.
func CompareValues(oldSchema, newSchema any, mode Mode) Report {
	c := &comparer{}
	c.compare("$", toNode(oldSchema), toNode(newSchema))
	sort.SliceStable(c.changes, func(i, j int) bool { return c.changes[i].Path < c.changes[j].Path })

	r := Report{Mode: mode, Compatible: true, Changes: c.changes}
	if r.Changes == nil {
		r.Changes = []Change{}
	}
	for i := range r.Changes {
		ch := &r.Changes[i]
		r.BreaksBackward = r.BreaksBackward || ch.BreaksBackward
		r.BreaksForward = r.BreaksForward || ch.BreaksForward
		ch.Breaking, ch.Reason = verdict(mode, ch.BreaksBackward, ch.BreaksForward)
		if ch.Breaking {
			r.BreakingCount++
			r.Compatible = false
		}
	}
	return r
}

func verdict(mode Mode, bb, bf bool) (bool, string) {
	switch {
	case mode.checksForward() && bf:
		return true, "breaking under " + string(mode) + ": consumers validating against the old version can receive documents that fail it"
	case mode.checksBackward() && bb:
		return true, "breaking under " + string(mode) + ": documents written under the old version (history, replays) can fail the new one"
	case mode == None:
		return false, "not checked: the dataset's compatibility mode is NONE"
	case bb || bf:
		return false, "safe under " + string(mode) + ": it only breaks the direction this mode does not require"
	default:
		return false, "safe in both directions"
	}
}

func decode(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// node is a schema position: it accepts everything, nothing, or what m says.
type node struct {
	anything bool
	nothing  bool
	m        map[string]any
}

var annotations = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "$anchor": true, "$defs": true,
	"title": true, "description": true, "default": true, "examples": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
}

var supported = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "enum": true, "const": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"minLength": true, "maxLength": true, "minItems": true, "maxItems": true,
	"pattern": true, "format": true, "uniqueItems": true,
}

func toNode(v any) node {
	switch t := v.(type) {
	case bool:
		if t {
			return node{anything: true}
		}
		return node{nothing: true}
	case map[string]any:
		for k := range t {
			if !annotations[k] {
				return node{m: t}
			}
		}
		return node{anything: true, m: t}
	}
	return node{anything: true}
}

type comparer struct{ changes []Change }

func (c *comparer) add(path, kind, detail string, bb, bf bool) {
	c.changes = append(c.changes, Change{Path: path, Kind: kind, Detail: detail, BreaksBackward: bb, BreaksForward: bf})
}

func (c *comparer) compare(path string, o, n node) {
	switch {
	case o.nothing && n.nothing:
		return
	case o.nothing:
		c.add(path, "allowed", "the new schema accepts values here that the old one rejected", false, true)
		return
	case n.nothing:
		c.add(path, "forbidden", "the new schema rejects every value here", true, false)
		return
	case o.anything && n.anything:
		return
	case o.anything:
		c.add(path, "constrained", "the old schema accepted any value here; the new one constrains it", true, false)
		return
	case n.anything:
		c.add(path, "unconstrained", "the old schema constrained this value; the new one accepts anything", false, true)
		return
	}

	c.unsupported(path, o.m, n.m)
	ot, nt := typeSet(o.m), typeSet(n.m)
	c.types(path, ot, nt)
	c.enums(path, o.m, n.m)
	c.bounds(path, o.m, n.m)
	c.assertions(path, o.m, n.m)
	if admits(ot, "object") && admits(nt, "object") {
		c.object(path, o.m, n.m)
	}
	if admits(ot, "array") && admits(nt, "array") {
		c.compare(path+"[]", sub(o.m, "items"), sub(n.m, "items"))
	}
}

func (c *comparer) unsupported(path string, o, n map[string]any) {
	keys := map[string]bool{}
	for k := range o {
		keys[k] = true
	}
	for k := range n {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		if supported[k] || annotations[k] {
			continue
		}
		if reflect.DeepEqual(o[k], n[k]) {
			continue
		}
		c.add(path, "unsupported_keyword",
			fmt.Sprintf("`%s` differs; the checker does not reason about it, so it is treated as breaking both ways", k), true, true)
	}
}

var allTypes = []string{"array", "boolean", "integer", "null", "number", "object", "string"}

func typeSet(m map[string]any) map[string]bool {
	s := map[string]bool{}
	switch t := m["type"].(type) {
	case string:
		s[t] = true
	case []any:
		for _, v := range t {
			if str, ok := v.(string); ok {
				s[str] = true
			}
		}
	default:
		for _, t := range allTypes {
			s[t] = true
		}
	}
	return s
}

// admits reports whether a type set accepts values of type t; number admits integer.
func admits(s map[string]bool, t string) bool {
	return s[t] || (t == "integer" && s["number"])
}

func (c *comparer) types(path string, ot, nt map[string]bool) {
	var lost, gained []string
	for _, t := range allTypes {
		if ot[t] && !admits(nt, t) {
			lost = append(lost, t)
		}
		if nt[t] && !admits(ot, t) {
			gained = append(gained, t)
		}
	}
	lostNull, gainedNull := remove(&lost, "null"), remove(&gained, "null")
	if lostNull {
		c.add(path, "nullable_removed", "null is no longer accepted", true, false)
	}
	if gainedNull {
		c.add(path, "nullable_added", "null is now accepted", false, true)
	}
	from, to := typeList(ot), typeList(nt)
	switch {
	case len(lost) > 0 && len(gained) > 0:
		c.add(path, "type_changed", fmt.Sprintf("type %s → %s", from, to), true, true)
	case len(lost) == 1 && lost[0] == "number" && nt["integer"]:
		c.add(path, "type_narrowed", "number → integer: fractional values are rejected", true, false)
	case len(lost) > 0:
		c.add(path, "type_narrowed", fmt.Sprintf("type %s → %s", from, to), true, false)
	case len(gained) == 1 && gained[0] == "number" && ot["integer"]:
		c.add(path, "type_widened", "integer → number: fractional values now pass", false, true)
	case len(gained) > 0:
		c.add(path, "type_widened", fmt.Sprintf("type %s → %s", from, to), false, true)
	}
}

func remove(s *[]string, v string) bool {
	for i, x := range *s {
		if x == v {
			*s = append((*s)[:i], (*s)[i+1:]...)
			return true
		}
	}
	return false
}

func typeList(s map[string]bool) string {
	if len(s) == len(allTypes) {
		return "any"
	}
	var out []string
	for _, t := range allTypes {
		if s[t] {
			out = append(out, t)
		}
	}
	return strings.Join(out, "|")
}

func enumValues(m map[string]any) ([]string, bool) {
	if v, ok := m["const"]; ok {
		return []string{canon(v)}, true
	}
	arr, ok := m["enum"].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		out = append(out, canon(v))
	}
	return out, true
}

func canon(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func (c *comparer) enums(path string, o, n map[string]any) {
	ov, oHas := enumValues(o)
	nv, nHas := enumValues(n)
	switch {
	case !oHas && !nHas:
		return
	case oHas && !nHas:
		c.add(path, "enum_dropped", "values are no longer restricted to "+strings.Join(ov, ", "), false, true)
		return
	case !oHas && nHas:
		c.add(path, "enum_introduced", "values are now restricted to "+strings.Join(nv, ", "), true, false)
		return
	}
	removed, added := diff(ov, nv), diff(nv, ov)
	if len(removed) > 0 {
		c.add(path, "enum_value_removed", "no longer allowed: "+strings.Join(removed, ", "), true, false)
	}
	if len(added) > 0 {
		c.add(path, "enum_value_added", "newly allowed: "+strings.Join(added, ", "), false, true)
	}
}

func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, v := range b {
		in[v] = true
	}
	var out []string
	for _, v := range a {
		if !in[v] {
			out = append(out, v)
		}
	}
	return out
}

var lowerBounds = []string{"minimum", "exclusiveMinimum", "minLength", "minItems"}
var upperBounds = []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems"}

func (c *comparer) bounds(path string, o, n map[string]any) {
	for _, k := range lowerBounds {
		c.bound(path, k, o, n, true)
	}
	for _, k := range upperBounds {
		c.bound(path, k, o, n, false)
	}
}

func (c *comparer) bound(path, key string, o, n map[string]any, lower bool) {
	ov, oHas := number(o[key])
	nv, nHas := number(n[key])
	var tightened, loosened bool
	switch {
	case !oHas && !nHas:
		return
	case !oHas:
		tightened = true
	case !nHas:
		loosened = true
	case nv == ov:
		return
	case lower:
		tightened, loosened = nv > ov, nv < ov
	default:
		tightened, loosened = nv < ov, nv > ov
	}
	detail := fmt.Sprintf("%s %s → %s", key, show(o[key], oHas), show(n[key], nHas))
	if tightened {
		c.add(path, "bound_tightened", detail, true, false)
	}
	if loosened {
		c.add(path, "bound_loosened", detail, false, true)
	}
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	}
	return 0, false
}

func show(v any, ok bool) string {
	if !ok {
		return "none"
	}
	return fmt.Sprint(v)
}

// assertions covers keywords compared by equality only: pattern and format
// (whose containment the checker does not attempt) and uniqueItems.
func (c *comparer) assertions(path string, o, n map[string]any) {
	for _, k := range []string{"pattern", "format"} {
		ov, oHas := o[k]
		nv, nHas := n[k]
		switch {
		case !oHas && !nHas:
		case !oHas:
			c.add(path, k+"_added", fmt.Sprintf("%s %v added", k, nv), true, false)
		case !nHas:
			c.add(path, k+"_removed", fmt.Sprintf("%s %v removed", k, ov), false, true)
		case !reflect.DeepEqual(ov, nv):
			c.add(path, k+"_changed",
				fmt.Sprintf("%s %v → %v; containment is not computed, so it is treated as breaking both ways", k, ov, nv), true, true)
		}
	}
	ou, _ := o["uniqueItems"].(bool)
	nu, _ := n["uniqueItems"].(bool)
	switch {
	case !ou && nu:
		c.add(path, "unique_items_added", "array items must now be unique", true, false)
	case ou && !nu:
		c.add(path, "unique_items_removed", "array items may now repeat", false, true)
	}
}

func (c *comparer) object(path string, o, n map[string]any) {
	op, np := props(o), props(n)
	oa, na := sub(o, "additionalProperties"), sub(n, "additionalProperties")
	oreq, nreq := requiredSet(o), requiredSet(n)

	keys := map[string]bool{}
	for k := range op {
		keys[k] = true
	}
	for k := range np {
		keys[k] = true
	}
	for k := range oreq {
		keys[k] = true
	}
	for k := range nreq {
		keys[k] = true
	}

	for _, k := range sortedKeys(keys) {
		p := path + "." + k
		os, inO := op[k]
		ns, inN := np[k]
		so, sn := oa, na
		if inO {
			so = toNode(os)
		}
		if inN {
			sn = toNode(ns)
		}
		becameRequired := !oreq[k] && nreq[k]
		becameOptional := oreq[k] && !nreq[k]

		switch {
		case !inO && inN:
			bb, bf := becameRequired, false
			var why []string
			switch {
			case so.nothing && !sn.nothing:
				bf = true
				why = append(why, "the old schema is closed (additionalProperties: false), so documents carrying it fail the old version")
			case so.anything && !sn.anything:
				bb = true
				why = append(why, "the old schema is open, so older documents could already carry this name with any value")
			case !so.anything && !so.nothing:
				c.compare(p, so, sn)
			}
			if becameRequired {
				why = append(why, "it is required, and documents written before it existed lack it")
			}
			c.add(p, "field_added", "field added: "+orNone(why), bb, bf)
		case inO && !inN:
			bb, bf := false, becameOptional
			var why []string
			switch {
			case sn.nothing && !so.nothing:
				bb = true
				why = append(why, "the new schema is closed, so documents that still carry it fail")
			case sn.anything && !so.anything:
				bf = true
				why = append(why, "the new schema is open, so new documents may carry any value under this name")
			case !sn.anything && !sn.nothing:
				c.compare(p, so, sn)
			}
			if becameOptional {
				why = append(why, "it was required, and consumers relying on it will find it missing")
			}
			c.add(p, "field_removed", "field removed: "+orNone(why), bb, bf)
		default:
			c.compare(p, so, sn)
			if becameRequired {
				c.add(p, "optional_to_required", "now required: documents without it fail", true, false)
			}
			if becameOptional {
				c.add(p, "required_to_optional", "now optional: consumers relying on it can find it missing", false, true)
			}
		}
	}

	switch {
	case oa.anything && na.nothing:
		c.add(path, "closed", "additionalProperties: false — documents with undeclared fields now fail", true, false)
	case oa.nothing && na.anything:
		c.add(path, "opened", "undeclared fields are now accepted", false, true)
	default:
		c.compare(path+".*", oa, na)
	}
}

func orNone(why []string) string {
	if len(why) == 0 {
		return "no effect on validity"
	}
	return strings.Join(why, "; ")
}

func props(m map[string]any) map[string]any {
	p, _ := m["properties"].(map[string]any)
	if p == nil {
		return map[string]any{}
	}
	return p
}

func sub(m map[string]any, key string) node {
	v, ok := m[key]
	if !ok {
		return node{anything: true}
	}
	return toNode(v)
}

func requiredSet(m map[string]any) map[string]bool {
	s := map[string]bool{}
	if arr, ok := m["required"].([]any); ok {
		for _, v := range arr {
			if str, ok := v.(string); ok {
				s[str] = true
			}
		}
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
