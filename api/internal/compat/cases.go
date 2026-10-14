package compat

import (
	_ "embed"
	"encoding/json"
)

//go:embed cases.json
var casesJSON []byte

// LabelledCase is one hand-labelled schema change.
type LabelledCase struct {
	Name  string          `json:"name"`
	Old   json.RawMessage `json:"old"`
	New   json.RawMessage `json:"new"`
	Label struct {
		Backward string `json:"backward"`
		Forward  string `json:"forward"`
	} `json:"label"`
	Note string `json:"note"`
	// KnownConservative marks cases the checker is expected to over-report
	// because they rely on keywords or regex containment it does not compute.
	KnownConservative bool `json:"known_conservative"`
}

// BreaksBackward is the label for the BACKWARD direction.
func (c LabelledCase) BreaksBackward() bool { return c.Label.Backward == "breaking" }

// BreaksForward is the label for the FORWARD direction.
func (c LabelledCase) BreaksForward() bool { return c.Label.Forward == "breaking" }

// LabelledCases returns the labelled set shipped with the checker.
func LabelledCases() ([]LabelledCase, error) {
	var f struct {
		Cases []LabelledCase `json:"cases"`
	}
	err := json.Unmarshal(casesJSON, &f)
	return f.Cases, err
}
