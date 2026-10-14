package compat

import (
	"strings"
	"testing"
)

func TestLabelledCases(t *testing.T) {
	cases, err := LabelledCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("expected at least 40 labelled cases, got %d", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, s := range [][]byte{c.Old, c.New} {
				if err := Validate(s); err != nil {
					t.Fatalf("case schema is not a valid contract: %v", err)
				}
			}

			// The labels must agree with the real validator on probe documents.
			o, err := Oracle(c.Old, c.New)
			if err != nil {
				t.Fatal(err)
			}
			if got := o.BreaksBackward != nil; got != c.BreaksBackward() {
				t.Errorf("label says backward=%s, oracle found witness=%v %v", c.Label.Backward, got, o.BreaksBackward)
			}
			if got := o.BreaksForward != nil; got != c.BreaksForward() {
				t.Errorf("label says forward=%s, oracle found witness=%v %v", c.Label.Forward, got, o.BreaksForward)
			}

			// The checker must never call a breaking change safe, and may only
			// over-report on cases marked as relying on what it cannot compute.
			r, err := Compare(c.Old, c.New, Full)
			if err != nil {
				t.Fatal(err)
			}
			if c.BreaksBackward() && !r.BreaksBackward {
				t.Errorf("false negative (backward): %+v", r.Changes)
			}
			if c.BreaksForward() && !r.BreaksForward {
				t.Errorf("false negative (forward): %+v", r.Changes)
			}
			over := (!c.BreaksBackward() && r.BreaksBackward) || (!c.BreaksForward() && r.BreaksForward)
			if over != c.KnownConservative {
				t.Errorf("over-reported=%v but known_conservative=%v: %+v", over, c.KnownConservative, r.Changes)
			}
		})
	}
}

func TestModesDecideBreaking(t *testing.T) {
	old := []byte(`{"type":"object","properties":{"status":{"type":"string","enum":["placed","shipped"]}}}`)
	added := []byte(`{"type":"object","properties":{"status":{"type":"string","enum":["placed","shipped","returned"]}}}`)
	for mode, want := range map[Mode]bool{Backward: true, Forward: false, Full: false, None: true} {
		r, err := Compare(old, added, mode)
		if err != nil {
			t.Fatal(err)
		}
		if r.Compatible != want {
			t.Errorf("%s: compatible=%v, want %v", mode, r.Compatible, want)
		}
	}
}

func TestReportExplainsTheChange(t *testing.T) {
	old := []byte(`{"type":"object","properties":{"amount":{"type":"integer"}},"required":["amount"]}`)
	nw := []byte(`{"type":"object","properties":{"amount":{"type":"number"}}}`)
	r, err := Compare(old, nw, Forward)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Change{}
	for _, c := range r.Changes {
		kinds[c.Kind] = c
	}
	if c, ok := kinds["type_widened"]; !ok || c.Path != "$.amount" || !c.Breaking {
		t.Errorf("want a breaking type_widened at $.amount, got %+v", r.Changes)
	}
	if c, ok := kinds["required_to_optional"]; !ok || !strings.Contains(c.Reason, "consumers") {
		t.Errorf("want required_to_optional with a consumer-facing reason, got %+v", r.Changes)
	}
	if r.BreakingCount != 2 || r.Compatible {
		t.Errorf("breaking=%d compatible=%v", r.BreakingCount, r.Compatible)
	}
}

func TestValidateRejectsNonContracts(t *testing.T) {
	for _, s := range []string{`not json`, `{"type": 12}`, `{"type":"string"}`, `[]`} {
		if err := Validate([]byte(s)); err == nil {
			t.Errorf("%s: want an error", s)
		}
	}
	if err := Validate([]byte(`{"type":"object"}`)); err != nil {
		t.Error(err)
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode(" forward "); err != nil || m != Forward {
		t.Errorf("got %v %v", m, err)
	}
	if _, err := ParseMode("transitive"); err == nil {
		t.Error("want error")
	}
}
