package official

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Matches what claude.ai shows as Session 79%, Weekly 72%, Weekly Fable 50%.
const newFormat = `{"limits":[
 {"kind":"session","percent":79,"resets_at":"2026-11-17T07:30:00Z"},
 {"kind":"weekly_all","percent":72,"resets_at":"2026-11-20T22:59:00Z"},
 {"kind":"weekly_scoped","percent":50,"resets_at":"2026-11-20T23:00:00Z","scope":{"model":{"display_name":"Fable"}}}
]}`

const oldFormat = `{"five_hour":{"utilization":79,"resets_at":"2026-11-17T07:30:00Z"},
 "seven_day":{"utilization":72,"resets_at":"2026-11-20T22:59:00Z"},
 "seven_day_opus":{"utilization":10,"resets_at":null},"seven_day_sonnet":null}`

func TestParseNewFormat(t *testing.T) {
	u, err := Parse([]byte(newFormat))
	if err != nil {
		t.Fatal(err)
	}
	if u.Session == nil || u.Session.Percent != 79 || u.Session.ResetsAt.IsZero() {
		t.Fatalf("session: %+v", u.Session)
	}
	if u.Weekly == nil || u.Weekly.Percent != 72 {
		t.Fatalf("weekly: %+v", u.Weekly)
	}
	if len(u.Scoped) != 1 || u.Scoped[0].Name != "Weekly Fable" || u.Scoped[0].Percent != 50 {
		t.Fatalf("scoped: %+v", u.Scoped)
	}
	if h := u.Highest(); h == nil || h.Percent != 79 {
		t.Fatalf("highest: %+v", h)
	}
}

func TestParseOldFormat(t *testing.T) {
	u, err := Parse([]byte(oldFormat))
	if err != nil {
		t.Fatal(err)
	}
	if u.Session.Percent != 79 || u.Weekly.Percent != 72 || len(u.Scoped) != 1 || u.Scoped[0].Name != "Weekly Opus" {
		t.Fatalf("old format: %+v", u)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte(`{"five_hour":null,"seven_day":null}`)); err == nil {
		t.Fatal("empty usage must be an error")
	}
}

func TestClientFlow(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), "sessionKey=test-key") {
			sawCookie = true
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			w.Write([]byte(`[{"uuid":"api-org","capabilities":["api"]},{"uuid":"chat-org","capabilities":["chat","claude_pro"]}]`))
		case "/api/organizations/chat-org/usage":
			w.Write([]byte(newFormat))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{SessionKey: "test-key", HTTP: &http.Client{Transport: rewrite{srv.URL}}}
	u, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sawCookie || c.OrgID != "chat-org" || u.Session.Percent != 79 {
		t.Fatalf("flow: org=%s usage=%+v", c.OrgID, u)
	}
	bad := &Client{SessionKey: "wrong", HTTP: &http.Client{Transport: rewrite{srv.URL}}}
	if _, err := bad.Fetch(context.Background()); err != ErrAuth {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

// rewrite sends claude.ai requests to the test server.
type rewrite struct{ base string }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = "http", strings.TrimPrefix(rw.base, "http://")
	r2.URL = &u
	r2.Host = u.Host
	return http.DefaultTransport.RoundTrip(r2)
}
