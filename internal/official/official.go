// Package official fetches the plan usage that claude.ai itself shows
// (Session 5h, Weekly 7-day, and per-model weekly limits) from
// https://claude.ai/api/organizations/{org}/usage, authenticated with the
// user's claude.ai session key.
//
// This endpoint is what the claude.ai web app uses. It is not a documented public
// API and can change; callers should fall back to the local estimate on error.
//
// The session key grants full access to the claude.ai account. It is only ever sent
// to claude.ai and is never logged.
package official

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const baseURL = "https://claude.ai/api"

// Limit is one usage bar.
type Limit struct {
	Name     string    // "Session (5hr)", "Weekly (7 day)", "Weekly Fable", ...
	Percent  float64   // 0-100
	ResetsAt time.Time // zero if unknown
}

// Usage is the set of bars claude.ai reports.
type Usage struct {
	Session *Limit
	Weekly  *Limit
	Scoped  []Limit // per-model weekly limits, sorted by name
}

// All returns every bar in display order.
func (u Usage) All() []Limit {
	var out []Limit
	if u.Session != nil {
		out = append(out, *u.Session)
	}
	if u.Weekly != nil {
		out = append(out, *u.Weekly)
	}
	return append(out, u.Scoped...)
}

// Highest returns the fullest bar, or nil if there are none.
func (u Usage) Highest() *Limit {
	var best *Limit
	for _, l := range u.All() {
		l := l
		if best == nil || l.Percent > best.Percent {
			best = &l
		}
	}
	return best
}

type window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type limitEntry struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type usageResponse struct {
	Limits         []limitEntry `json:"limits"`
	FiveHour       *window      `json:"five_hour"`
	SevenDay       *window      `json:"seven_day"`
	SevenDayOpus   *window      `json:"seven_day_opus"`
	SevenDaySonnet *window      `json:"seven_day_sonnet"`
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Parse decodes a /usage response. It understands both the current `limits` array
// and the older five_hour / seven_day fields.
func Parse(body []byte) (Usage, error) {
	var r usageResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return Usage{}, fmt.Errorf("unexpected usage response: %w", err)
	}
	var u Usage
	if len(r.Limits) > 0 {
		for _, e := range r.Limits {
			if e.Percent == nil {
				continue
			}
			l := Limit{Percent: *e.Percent, ResetsAt: parseTime(e.ResetsAt)}
			switch e.Kind {
			case "session":
				l.Name = "Session (5hr)"
				u.Session = &l
			case "weekly_all":
				l.Name = "Weekly (7 day)"
				u.Weekly = &l
			case "weekly_scoped":
				name := "model"
				if e.Scope != nil && e.Scope.Model != nil && e.Scope.Model.DisplayName != "" {
					name = e.Scope.Model.DisplayName
				}
				l.Name = "Weekly " + title(name)
				u.Scoped = append(u.Scoped, l)
			}
		}
	} else {
		w := func(win *window, name string) *Limit {
			if win == nil || win.Utilization == nil {
				return nil
			}
			return &Limit{Name: name, Percent: *win.Utilization, ResetsAt: parseTime(win.ResetsAt)}
		}
		u.Session = w(r.FiveHour, "Session (5hr)")
		u.Weekly = w(r.SevenDay, "Weekly (7 day)")
		if l := w(r.SevenDayOpus, "Weekly Opus"); l != nil {
			u.Scoped = append(u.Scoped, *l)
		}
		if l := w(r.SevenDaySonnet, "Weekly Sonnet"); l != nil {
			u.Scoped = append(u.Scoped, *l)
		}
	}
	sort.Slice(u.Scoped, func(i, j int) bool { return u.Scoped[i].Name < u.Scoped[j].Name })
	if u.Session == nil && u.Weekly == nil && len(u.Scoped) == 0 {
		return u, errors.New("claude.ai reported no usage limits for this account")
	}
	return u, nil
}

// ErrAuth means the session key was rejected (expired, logged out, or blocked).
var ErrAuth = errors.New("claude.ai rejected the session key (expired or logged out?)")

// Client calls claude.ai.
type Client struct {
	SessionKey string
	OrgID      string // optional; looked up and cached when empty
	HTTP       *http.Client
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "sessionKey="+c.SessionKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36")
	req.Header.Set("Referer", "https://claude.ai/settings/usage")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach claude.ai: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrAuth
	case resp.StatusCode == http.StatusForbidden:
		if strings.Contains(strings.ToLower(string(body)), "cloudflare") || strings.Contains(string(body), "challenge") {
			return nil, errors.New("claude.ai blocked the request (Cloudflare check); try again later")
		}
		return nil, ErrAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("claude.ai returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// orgID returns the organization to read usage for: the configured one, or the
// first organization on the account that can chat.
func (c *Client) orgID(ctx context.Context) (string, error) {
	if c.OrgID != "" {
		return c.OrgID, nil
	}
	body, err := c.get(ctx, "/organizations")
	if err != nil {
		return "", err
	}
	var orgs []struct {
		UUID         string   `json:"uuid"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil || len(orgs) == 0 {
		return "", errors.New("no claude.ai organization found for this session")
	}
	pick := orgs[0].UUID
	for _, o := range orgs {
		for _, cap := range o.Capabilities {
			if cap == "chat" || cap == "claude_pro" || cap == "claude_max" {
				pick = o.UUID
				break
			}
		}
	}
	c.OrgID = pick
	return pick, nil
}

// Fetch returns the current plan usage.
func (c *Client) Fetch(ctx context.Context) (Usage, error) {
	if strings.TrimSpace(c.SessionKey) == "" {
		return Usage{}, errors.New("no claude.ai session key set")
	}
	org, err := c.orgID(ctx)
	if err != nil {
		return Usage{}, err
	}
	body, err := c.get(ctx, "/organizations/"+org+"/usage")
	if err != nil {
		return Usage{}, err
	}
	return Parse(body)
}
