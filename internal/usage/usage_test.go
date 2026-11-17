package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func line(ts, id, req, model string, in, out, cw, cr int64) string {
	return `{"type":"assistant","timestamp":"` + ts + `","requestId":"` + req + `","message":{"id":"` + id + `","model":"` + model +
		`","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `,"cache_creation_input_tokens":` + itoa(cw) +
		`,"cache_read_input_tokens":` + itoa(cr) + `}}}` + "\n"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestParseLine(t *testing.T) {
	_, e, ok := ParseLine([]byte(line("2026-11-17T08:00:00.000Z", "msg_1", "req_1", "claude-opus-5-5", 10, 20, 30, 40)))
	if !ok || e.Input != 10 || e.Output != 20 || e.CacheWrite5m != 30 || e.CacheRead != 40 || e.Tokens() != 60 {
		t.Fatalf("bad parse: %+v ok=%v", e, ok)
	}
	if _, _, ok := ParseLine([]byte(`{"type":"user","message":{"content":"usage"}}`)); ok {
		t.Fatal("user line must not parse")
	}
	// cache_creation details split 5m / 1h
	l := `{"type":"assistant","timestamp":"2026-11-17T08:00:00Z","requestId":"r","message":{"id":"m","model":"claude-sonnet-5-5","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":300,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`
	_, e, ok = ParseLine([]byte(l))
	if !ok || e.CacheWrite5m != 100 || e.CacheWrite1h != 200 {
		t.Fatalf("bad cache split: %+v", e)
	}
}

func TestPricing(t *testing.T) {
	pt := DefaultPrices()
	pr, ok := pt.For("claude-opus-5-5")
	if !ok || pr.Input != 4 || pr.Output != 20 {
		t.Fatalf("opus 5.5 price: %+v", pr)
	}
	if pr, _ := pt.For("claude-opus-4-8"); pr.Input != 5 {
		t.Fatalf("opus 4.x prefix price: %+v", pr)
	}
	if pr, _ := pt.For("claude-sonnet-5"); pr.CacheRead != 0.20 {
		t.Fatalf("sonnet 5 must not match sonnet 5.5: %+v", pr)
	}
	c := pr.Cost(Entry{Input: 1_000_000}) // pr is still the Opus 5.5 price ($4 per 1M input)
	if c != 4 {
		t.Fatalf("cost = %v", c)
	}
}

func TestScanDedupeAndIncremental(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(proj, "s.jsonl")
	now := time.Now().UTC()
	ts := now.Add(-time.Hour).Format(time.RFC3339Nano)
	// same response logged twice (streamed content blocks) must count once
	data := line(ts, "msg_a", "req_a", "claude-opus-5-5", 100, 50, 0, 0) + line(ts, "msg_a", "req_a", "claude-opus-5-5", 100, 50, 0, 0)
	if err := os.WriteFile(f, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewScanner([]string{dir}, 8*24*time.Hour, DefaultPrices())
	if err := s.Refresh(now); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Entries()); n != 1 {
		t.Fatalf("want 1 entry, got %d", n)
	}
	// append a partial line: must not be read until it is complete
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	full := line(ts, "msg_b", "req_b", "claude-opus-5-5", 1, 1, 0, 0)
	fh.WriteString(full[:20])
	fh.Close()
	s.Refresh(now)
	if n := len(s.Entries()); n != 1 {
		t.Fatalf("partial line read: %d entries", n)
	}
	fh, _ = os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	fh.WriteString(full[20:])
	fh.Close()
	s.Refresh(now)
	if n := len(s.Entries()); n != 2 {
		t.Fatalf("want 2 entries after completing the line, got %d", n)
	}
}

func TestBlocksAndSummary(t *testing.T) {
	base := time.Date(2026, 11, 17, 8, 20, 0, 0, time.UTC)
	mk := func(off time.Duration, tok int64) Entry { return Entry{Time: base.Add(off), Model: "m", Input: tok} }
	entries := []Entry{
		mk(0, 100), mk(time.Hour, 100), // block 1: 08:00-13:00
		mk(5*time.Hour, 50),             // 13:20 -> past 13:00 end: block 2 starts 13:00
		mk(11*time.Hour, 400),           // 19:20 -> gap >= 5h: block 3 (19:00-24:00)
	}
	bs := Blocks(entries)
	if len(bs) != 3 || bs[0].Tokens != 200 || bs[1].Tokens != 50 || bs[2].Tokens != 400 {
		t.Fatalf("blocks: %+v", bs)
	}
	if !bs[0].Start.Equal(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("block start not floored to hour: %v", bs[0].Start)
	}
	now := base.Add(11*time.Hour + 30*time.Minute)
	s := Summarize(entries, now, 0)
	if s.Active == nil || s.Active.Tokens != 400 {
		t.Fatalf("active block: %+v", s.Active)
	}
	if !s.AutoLimit || s.Limit != 200 || s.Percent != 200 {
		t.Fatalf("auto limit: limit=%d pct=%v", s.Limit, s.Percent)
	}
	s = Summarize(entries, now, 800)
	if s.Percent != 50 {
		t.Fatalf("fixed limit pct=%v", s.Percent)
	}
	idle := Summarize(entries, base.Add(30*time.Hour), 800)
	if idle.Active != nil || idle.Percent != 0 {
		t.Fatalf("idle summary: %+v", idle)
	}
}
