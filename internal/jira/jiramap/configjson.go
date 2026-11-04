package jiramap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// configWire is Config's persisted spelling (task 142.6): the mapping
// override a project's issue-source binding carries in
// projects.jira_mapping_json. snake_case like every other vincent wire
// shape; Config itself stays a plain value with no tags, so this is the one
// place the spelling lives.
type configWire struct {
	Overrides             *overridesWire `json:"overrides,omitempty"`
	DuplicateResolutions  []string       `json:"duplicate_resolutions,omitempty"`
	NotPlannedResolutions []string       `json:"not_planned_resolutions,omitempty"`
}

type overridesWire struct {
	CloseCompleted  string `json:"close_completed,omitempty"`
	CloseNotPlanned string `json:"close_not_planned,omitempty"`
	CloseDuplicate  string `json:"close_duplicate,omitempty"`
	Reopen          string `json:"reopen,omitempty"`
}

// ParseConfig decodes a mapping override strictly: an unknown field, a
// wrong type, trailing data or a non-object is an error, so a typo in a
// binding fails where it was written rather than silently falling back to
// the heuristic. Empty input is the zero Config (the defaults).
func ParseConfig(data []byte) (Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w *configWire
	if err := dec.Decode(&w); err != nil {
		return Config{}, fmt.Errorf("mapping: %w", err)
	}
	if w == nil {
		return Config{}, errors.New("mapping: must be a JSON object")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("mapping: trailing data after the JSON object")
	}
	c := Config{
		DuplicateResolutions:  w.DuplicateResolutions,
		NotPlannedResolutions: w.NotPlannedResolutions,
	}
	if o := w.Overrides; o != nil {
		c.Overrides = Overrides{
			CloseCompleted: o.CloseCompleted, CloseNotPlanned: o.CloseNotPlanned,
			CloseDuplicate: o.CloseDuplicate, Reopen: o.Reopen,
		}
	}
	return c, nil
}
