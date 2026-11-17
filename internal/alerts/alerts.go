// Package alerts decides when to warn the user that a usage limit crossed a step
// (by default every 25%). Each step fires once per limit per usage window; when a
// window resets the steps start again. State is persisted so restarts do not re-alert.
package alerts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Limit is one usage bar to watch.
type Limit struct {
	Name     string
	Percent  float64
	ResetsAt time.Time
}

// Seen records the highest step already announced for a limit in its current window.
type Seen struct {
	ResetsAt time.Time `json:"resetsAt"`
	Step     float64   `json:"step"`
}

// State is the persisted alert history.
type State struct {
	Limits map[string]Seen `json:"limits"`
}

// Alert is one step crossing to announce.
type Alert struct {
	Limit Limit
	Step  float64
}

// windowTolerance absorbs small drifts in the reported reset time of the same window.
const windowTolerance = 10 * time.Minute

func sameWindow(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() == b.IsZero()
	}
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= windowTolerance
}

// Check compares the current limits with what was already announced and returns the
// new crossings. Only the highest newly reached step is returned per limit, so jumping
// from 20% to 80% gives one alert (75%), not three.
func (s *State) Check(limits []Limit, steps []float64) []Alert {
	if s.Limits == nil {
		s.Limits = map[string]Seen{}
	}
	st := append([]float64(nil), steps...)
	sort.Float64s(st)
	var out []Alert
	for _, l := range limits {
		seen := s.Limits[l.Name]
		if !sameWindow(seen.ResetsAt, l.ResetsAt) {
			seen = Seen{ResetsAt: l.ResetsAt}
		}
		reached := 0.0
		for _, step := range st {
			if step > 0 && l.Percent >= step {
				reached = step
			}
		}
		if reached > seen.Step {
			out = append(out, Alert{Limit: l, Step: reached})
			seen.Step = reached
		}
		if !l.ResetsAt.IsZero() {
			seen.ResetsAt = l.ResetsAt
		}
		s.Limits[l.Name] = seen
	}
	return out
}

// Load reads the state file; a missing or broken file gives an empty state.
func Load(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Limits == nil {
		s.Limits = map[string]Seen{}
	}
	return s
}

// Save writes the state file.
func (s State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(path, b, 0o644)
}
