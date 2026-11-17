// Package usage reads the session logs that Claude Code writes locally
// (<claude dir>/projects/**/*.jsonl) and turns them into token and cost totals.
// It never talks to the network.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one billed model response.
type Entry struct {
	Time         time.Time
	Model        string
	Input        int64
	Output       int64
	CacheWrite5m int64
	CacheWrite1h int64
	CacheRead    int64
	Cost         float64
}

// Tokens is the number of tokens that count toward usage: input + output + cache writes.
// Cache reads are tracked separately because they are far cheaper and would dominate the total.
func (e Entry) Tokens() int64 { return e.Input + e.Output + e.CacheWrite5m + e.CacheWrite1h }

type rawLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheDetail   *struct {
				E5m int64 `json:"ephemeral_5m_input_tokens"`
				E1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	markUsage     = []byte(`"usage"`)
	markAssistant = []byte(`"assistant"`)
)

// ParseLine extracts a usage entry from one JSONL line.
// key identifies the response so the same response logged several times is counted once.
func ParseLine(line []byte) (key string, e Entry, ok bool) {
	if !bytes.Contains(line, markUsage) || !bytes.Contains(line, markAssistant) {
		return "", Entry{}, false
	}
	var r rawLine
	if err := json.Unmarshal(line, &r); err != nil {
		return "", Entry{}, false
	}
	if r.Type != "assistant" || r.Message == nil || r.Message.Usage == nil {
		return "", Entry{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		return "", Entry{}, false
	}
	u := r.Message.Usage
	e = Entry{Time: t, Model: r.Message.Model, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead}
	if u.CacheDetail != nil && u.CacheDetail.E5m+u.CacheDetail.E1h > 0 {
		e.CacheWrite5m, e.CacheWrite1h = u.CacheDetail.E5m, u.CacheDetail.E1h
	} else {
		e.CacheWrite5m = u.CacheCreation
	}
	if e.Tokens()+e.CacheRead == 0 {
		return "", Entry{}, false
	}
	key = r.Message.ID + ":" + r.RequestID
	if r.Message.ID == "" && r.RequestID == "" {
		key = r.Timestamp + ":" + r.Message.Model
	}
	return key, e, true
}

type fileState struct {
	offset int64
	size   int64
}

// Scanner incrementally reads Claude Code logs. After the first scan only the bytes
// appended since the previous refresh are read, so refreshing every minute is cheap.
type Scanner struct {
	Dirs   []string      // directories that contain a "projects" folder, e.g. ~/.claude
	MaxAge time.Duration // ignore entries (and files) older than this
	Prices PriceTable

	mu      sync.Mutex
	files   map[string]*fileState
	entries map[string]Entry
}

// NewScanner creates a scanner over the given Claude config directories.
func NewScanner(dirs []string, maxAge time.Duration, prices PriceTable) *Scanner {
	return &Scanner{Dirs: dirs, MaxAge: maxAge, Prices: prices, files: map[string]*fileState{}, entries: map[string]Entry{}}
}

// DefaultDirs returns the Claude Code config directories to read: CLAUDE_CONFIG_DIR
// (comma-separated) if set, otherwise ~/.claude and ~/.config/claude.
func DefaultDirs() []string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		var out []string
		for _, d := range strings.Split(v, ",") {
			if d = strings.TrimSpace(d); d != "" {
				out = append(out, d)
			}
		}
		return out
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".claude"), filepath.Join(home, ".config", "claude")}
}

// Refresh reads any new log data. It is safe to call from multiple goroutines.
func (s *Scanner) Refresh(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-s.MaxAge)
	found := false
	for _, dir := range s.Dirs {
		root := filepath.Join(dir, "projects")
		if _, err := os.Stat(root); err != nil {
			continue
		}
		found = true
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			st := s.files[path]
			if st == nil {
				if info.ModTime().Before(cutoff) {
					return nil // old file, nothing in it can be in range
				}
				st = &fileState{}
				s.files[path] = st
			}
			if info.Size() < st.offset {
				st.offset = 0 // file was rewritten
			}
			if info.Size() == st.offset {
				return nil
			}
			s.readFrom(path, st, cutoff)
			return nil
		})
	}
	for k, e := range s.entries {
		if e.Time.Before(cutoff) {
			delete(s.entries, k)
		}
	}
	if !found {
		return errors.New("no Claude Code logs found (looked for a 'projects' folder in " + strings.Join(s.Dirs, ", ") + ")")
	}
	return nil
}

func (s *Scanner) readFrom(path string, st *fileState, cutoff time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 1<<20)
	pos := st.offset
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			pos += int64(len(line))
			if key, e, ok := ParseLine(line); ok && !e.Time.Before(cutoff) {
				if pr, ok := s.Prices.For(e.Model); ok {
					e.Cost = pr.Cost(e)
				}
				s.entries[key] = e
			}
		}
		if err != nil {
			break // EOF, or a partial last line that will be re-read next time
		}
	}
	st.offset = pos
}

// Entries returns all entries in time order.
func (s *Scanner) Entries() []Entry {
	s.mu.Lock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}
