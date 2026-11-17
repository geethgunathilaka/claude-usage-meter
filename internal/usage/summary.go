package usage

import (
	"sort"
	"time"
)

// BlockLength is the length of a Claude usage session window.
const BlockLength = 5 * time.Hour

// Block is one 5-hour usage window. A window starts at the hour of its first request
// and runs for five hours; a gap of five hours or more also starts a new window.
type Block struct {
	Start        time.Time
	End          time.Time
	First        time.Time
	LastActivity time.Time
	Requests     int
	Tokens       int64
	CacheRead    int64
	Cost         float64
	Models       map[string]int64
}

// Totals is a token and cost sum over a period.
type Totals struct {
	Requests  int
	Tokens    int64
	CacheRead int64
	Cost      float64
	Models    map[string]int64
}

func (t *Totals) add(e Entry) {
	if t.Models == nil {
		t.Models = map[string]int64{}
	}
	t.Requests++
	t.Tokens += e.Tokens()
	t.CacheRead += e.CacheRead
	t.Cost += e.Cost
	t.Models[e.Model] += e.Tokens()
}

// TopModel returns the model with the most tokens, or "" if there is no usage.
func (t Totals) TopModel() string { return topModel(t.Models) }

func topModel(m map[string]int64) string {
	best, bestN := "", int64(-1)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m[k] > bestN {
			best, bestN = k, m[k]
		}
	}
	return best
}

// Blocks groups time-ordered entries into 5-hour windows.
func Blocks(entries []Entry) []Block {
	var out []Block
	for _, e := range entries {
		n := len(out)
		if n == 0 || !e.Time.Before(out[n-1].End) || e.Time.Sub(out[n-1].LastActivity) >= BlockLength {
			start := e.Time.UTC().Truncate(time.Hour)
			out = append(out, Block{Start: start, End: start.Add(BlockLength), First: e.Time, Models: map[string]int64{}})
			n++
		}
		b := &out[n-1]
		b.LastActivity = e.Time
		b.Requests++
		b.Tokens += e.Tokens()
		b.CacheRead += e.CacheRead
		b.Cost += e.Cost
		b.Models[e.Model] += e.Tokens()
	}
	return out
}

// Summary is everything the tray needs to display.
type Summary struct {
	Now        time.Time
	Active     *Block  // current 5-hour window, nil when idle
	Limit      int64   // token limit used for Percent
	AutoLimit  bool    // Limit was derived from the largest past window
	Percent    float64 // Active.Tokens / Limit * 100, or -1 when unknown
	BurnPerMin float64 // tokens per minute in the active window
	Today      Totals
	Week       Totals // rolling 7 days
	PeakBlock  int64  // largest window in the scanned period
}

// Summarize builds a Summary. If limit <= 0 the largest completed window is used as the limit.
func Summarize(entries []Entry, now time.Time, limit int64) Summary {
	s := Summary{Now: now, Limit: limit, Percent: -1}
	blocks := Blocks(entries)
	if n := len(blocks); n > 0 {
		last := blocks[n-1]
		if now.Before(last.End) && now.Sub(last.LastActivity) < BlockLength {
			s.Active = &last
			blocks = blocks[:n-1]
		}
	}
	for _, b := range blocks {
		if b.Tokens > s.PeakBlock {
			s.PeakBlock = b.Tokens
		}
	}
	if s.Active != nil && s.Active.Tokens > s.PeakBlock && len(blocks) == 0 {
		s.PeakBlock = s.Active.Tokens
	}
	if s.Limit <= 0 && len(blocks) > 0 {
		s.Limit, s.AutoLimit = s.PeakBlock, true
	}
	if s.Active != nil {
		if s.Limit > 0 {
			s.Percent = float64(s.Active.Tokens) / float64(s.Limit) * 100
		}
		mins := now.Sub(s.Active.First).Minutes()
		if mins < 1 {
			mins = 1
		}
		s.BurnPerMin = float64(s.Active.Tokens) / mins
	} else if s.Limit > 0 {
		s.Percent = 0
	}
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	weekStart := now.Add(-7 * 24 * time.Hour)
	for _, e := range entries {
		if !e.Time.Before(weekStart) {
			s.Week.add(e)
		}
		if !e.Time.Before(dayStart) {
			s.Today.add(e)
		}
	}
	return s
}
