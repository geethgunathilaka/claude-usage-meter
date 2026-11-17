package alerts

import (
	"path/filepath"
	"testing"
	"time"
)

var steps = []float64{25, 50, 75, 100}

func TestStepsFireOncePerWindow(t *testing.T) {
	reset := time.Date(2026, 11, 17, 13, 0, 0, 0, time.UTC)
	var s State
	check := func(pct float64, r time.Time) []Alert {
		return s.Check([]Limit{{Name: "Session (5hr)", Percent: pct, ResetsAt: r}}, steps)
	}
	if a := check(10, reset); len(a) != 0 {
		t.Fatalf("10%%: %v", a)
	}
	if a := check(26, reset); len(a) != 1 || a[0].Step != 25 {
		t.Fatalf("26%%: %v", a)
	}
	if a := check(30, reset); len(a) != 0 {
		t.Fatalf("30%% must not repeat 25: %v", a)
	}
	// jump straight past 50 and 75: one alert for the highest step
	if a := check(80, reset); len(a) != 1 || a[0].Step != 75 {
		t.Fatalf("80%%: %v", a)
	}
	// small drift in reset time = same window, no re-alert
	if a := check(81, reset.Add(time.Minute)); len(a) != 0 {
		t.Fatalf("drift: %v", a)
	}
	if a := check(100, reset); len(a) != 1 || a[0].Step != 100 {
		t.Fatalf("100%%: %v", a)
	}
	// new window: steps start again
	next := reset.Add(5 * time.Hour)
	if a := check(5, next); len(a) != 0 {
		t.Fatalf("new window 5%%: %v", a)
	}
	if a := check(52, next); len(a) != 1 || a[0].Step != 50 {
		t.Fatalf("new window 52%%: %v", a)
	}
}

func TestLimitsAreIndependentAndPersisted(t *testing.T) {
	r := time.Date(2026, 11, 21, 4, 29, 0, 0, time.UTC)
	var s State
	a := s.Check([]Limit{{Name: "Weekly (7 day)", Percent: 74, ResetsAt: r}, {Name: "Weekly Fable", Percent: 50, ResetsAt: r}}, steps)
	if len(a) != 2 || a[0].Step != 50 || a[1].Step != 50 {
		t.Fatalf("independent limits: %v", a)
	}
	p := filepath.Join(t.TempDir(), "state.json")
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	s2 := Load(p)
	if a := s2.Check([]Limit{{Name: "Weekly (7 day)", Percent: 74, ResetsAt: r}}, steps); len(a) != 0 {
		t.Fatalf("restart re-alerted: %v", a)
	}
}
