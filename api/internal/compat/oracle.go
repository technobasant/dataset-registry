package compat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Witness is a document that one schema accepts and the other rejects.
type Witness struct {
	Document string `json:"document"`
}

// OracleResult is what the real validator says about two schemas, judged on
// generated probe documents rather than on the schemas' structure.
//
// The oracle can only prove breakage: a witness is a fact, the absence of one
// means no probe found it. It exists to check the labels and the checker
// against something other than the checker's own reasoning.
type OracleResult struct {
	Probes         int      `json:"probes"`
	BreaksBackward *Witness `json:"breaks_backward,omitempty"`
	BreaksForward  *Witness `json:"breaks_forward,omitempty"`
}

// Oracle validates every probe generated from both schemas against both.
func Oracle(oldSchema, newSchema []byte) (OracleResult, error) {
	oc, err := Compile(oldSchema)
	if err != nil {
		return OracleResult{}, fmt.Errorf("old: %w", err)
	}
	nc, err := Compile(newSchema)
	if err != nil {
		return OracleResult{}, fmt.Errorf("new: %w", err)
	}
	ov, _ := decode(oldSchema)
	nv, _ := decode(newSchema)

	probes := candidates([]any{ov, nv}, 0)
	res := OracleResult{Probes: len(probes)}
	for _, p := range probes {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(p))
		if err != nil {
			continue
		}
		okOld := oc.Validate(inst) == nil
		okNew := nc.Validate(inst) == nil
		if okOld && !okNew && res.BreaksBackward == nil {
			res.BreaksBackward = &Witness{Document: p}
		}
		if okNew && !okOld && res.BreaksForward == nil {
			res.BreaksForward = &Witness{Document: p}
		}
	}
	return res, nil
}

const maxProbeDepth = 4

var palette = []any{
	nil, true, false,
	json.Number("0"), json.Number("1"), json.Number("-1"), json.Number("2.5"), json.Number("1000000"),
	"", "a", "A", "2026-01-01T00:00:00Z", "not-a-date", strings.Repeat("x", 300),
	[]any{}, map[string]any{},
}

// candidates returns canonical JSON documents to try at a position governed by
// the given schemas (one per version; any may be a boolean or absent).
func candidates(schemas []any, depth int) []string {
	seen := map[string]bool{}
	var out []string
	emit := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		if s := string(b); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, v := range palette {
		emit(v)
	}
	if depth > maxProbeDepth {
		return out
	}

	var maps []map[string]any
	for _, s := range schemas {
		if m, ok := s.(map[string]any); ok {
			maps = append(maps, m)
		}
	}
	for _, m := range maps {
		for _, v := range literalValues(m) {
			emit(v)
		}
	}

	// Objects: start from each schema's exemplar (all declared fields), then
	// change one field at a time to every candidate for that field, or drop it,
	// or add an undeclared one.
	keys := map[string]bool{}
	for _, m := range maps {
		for k := range props(m) {
			keys[k] = true
		}
		for k := range requiredSet(m) {
			keys[k] = true
		}
	}
	if len(keys) > 0 {
		var bases []map[string]any
		for _, m := range maps {
			if b, ok := exemplar(m, depth).(map[string]any); ok {
				bases = append(bases, b, required(m, b))
			}
		}
		for _, base := range bases {
			emit(base)
			extra := clone(base)
			extra["zz_undeclared"] = "a"
			emit(extra)
			for _, k := range sortedKeys(keys) {
				var subs []any
				for _, m := range maps {
					if s, ok := props(m)[k]; ok {
						subs = append(subs, s)
					}
				}
				for _, c := range candidates(subs, depth+1) {
					var v any
					_ = json.Unmarshal([]byte(c), &v)
					mod := clone(base)
					mod[k] = restoreNumbers(v)
					emit(mod)
				}
				drop := clone(base)
				delete(drop, k)
				emit(drop)
			}
		}
	}

	// Arrays: empty, one exemplar, and one of each candidate for the items.
	var items []any
	for _, m := range maps {
		if it, ok := m["items"]; ok {
			items = append(items, it)
		}
	}
	if len(items) > 0 {
		for _, c := range candidates(items, depth+1) {
			var v any
			_ = json.Unmarshal([]byte(c), &v)
			emit([]any{v})
			emit([]any{v, v})
		}
	}
	return out
}

// restoreNumbers turns float64 back into json.Number after a round trip through
// Unmarshal, so a probe keeps its exact textual form (1 stays 1, not 1.0).
func restoreNumbers(v any) any {
	switch t := v.(type) {
	case float64:
		return json.Number(fmt.Sprint(t))
	case map[string]any:
		for k, x := range t {
			t[k] = restoreNumbers(x)
		}
	case []any:
		for i, x := range t {
			t[i] = restoreNumbers(x)
		}
	}
	return v
}

// literalValues are values a schema names explicitly: enum and const values,
// examples, and the edges of numeric and length bounds.
func literalValues(m map[string]any) []any {
	var out []any
	if arr, ok := m["enum"].([]any); ok {
		out = append(out, arr...)
	}
	if v, ok := m["const"]; ok {
		out = append(out, v)
	}
	if arr, ok := m["examples"].([]any); ok {
		out = append(out, arr...)
	}
	for _, k := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if f, ok := number(m[k]); ok {
			for _, d := range []float64{-1, -0.5, 0, 0.5, 1} {
				out = append(out, json.Number(fmt.Sprint(f+d)))
			}
		}
	}
	for _, k := range []string{"minLength", "maxLength"} {
		if f, ok := number(m[k]); ok {
			for _, d := range []int{-1, 0, 1} {
				if n := int(f) + d; n >= 0 {
					out = append(out, strings.Repeat("a", n))
				}
			}
		}
	}
	for _, k := range []string{"minItems", "maxItems"} {
		if f, ok := number(m[k]); ok {
			for _, d := range []int{-1, 0, 1} {
				if n := int(f) + d; n >= 0 {
					arr := make([]any, n)
					for i := range arr {
						arr[i] = exemplar(sub(m, "items").m, 0)
						if arr[i] == nil {
							arr[i] = json.Number(fmt.Sprint(i))
						}
					}
					out = append(out, arr)
				}
			}
		}
	}
	return out
}

// exemplar builds a value that the schema should accept, with every declared
// property present.
func exemplar(m map[string]any, depth int) any {
	if m == nil {
		return "a"
	}
	if v, ok := m["const"]; ok {
		return v
	}
	if arr, ok := m["enum"].([]any); ok && len(arr) > 0 {
		return arr[0]
	}
	if arr, ok := m["examples"].([]any); ok && len(arr) > 0 {
		return arr[0]
	}
	ts := typeSet(m)
	switch {
	case ts["object"] && (len(props(m)) > 0 || len(ts) < len(allTypes)):
		obj := map[string]any{}
		if depth > maxProbeDepth {
			return obj
		}
		p := props(m)
		for _, k := range sortedMapKeys(p) {
			sm, _ := p[k].(map[string]any)
			obj[k] = exemplar(sm, depth+1)
		}
		for k := range requiredSet(m) {
			if _, ok := obj[k]; !ok {
				obj[k] = "a"
			}
		}
		return obj
	case ts["string"]:
		n := 1
		if f, ok := number(m["minLength"]); ok && int(f) > n {
			n = int(f)
		}
		switch m["format"] {
		case "date-time":
			return "2026-01-01T00:00:00Z"
		case "date":
			return "2026-01-01"
		case "email":
			return "a@example.com"
		case "uuid":
			return "6f1c1d0e-8a7b-4c39-9d7e-0b5c3a2f1e4d"
		}
		return strings.Repeat("a", n)
	case ts["integer"]:
		if f, ok := number(m["minimum"]); ok {
			return json.Number(fmt.Sprint(int64(f)))
		}
		return json.Number("1")
	case ts["number"]:
		if f, ok := number(m["minimum"]); ok {
			return json.Number(fmt.Sprint(f + 0.5))
		}
		return json.Number("1.5")
	case ts["boolean"]:
		return true
	case ts["array"]:
		n := 1
		if f, ok := number(m["minItems"]); ok && int(f) > n {
			n = int(f)
		}
		it, _ := m["items"].(map[string]any)
		arr := make([]any, n)
		for i := range arr {
			arr[i] = exemplar(it, depth+1)
		}
		return arr
	case ts["null"]:
		return nil
	}
	return "a"
}

// required keeps only the schema's required properties of an exemplar.
func required(m map[string]any, ex map[string]any) map[string]any {
	req := requiredSet(m)
	out := map[string]any{}
	for k, v := range ex {
		if req[k] {
			out[k] = v
		}
	}
	return out
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
