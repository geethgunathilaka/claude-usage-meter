// Package format turns numbers into short labels for the tray menu.
package format

import (
	"fmt"
	"math"
	"time"
)

// Tokens formats a token count as 950, 12.3k, 4.56M or 1.2B.
func Tokens(n int64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case f >= 1e6:
		return fmt.Sprintf("%.2fM", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.1fk", f/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// Money formats US dollars.
func Money(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// Percent formats a percentage, or "—" when unknown (negative).
func Percent(p float64) string {
	if p < 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", math.Floor(p))
}

// Until formats the time left until t, e.g. "2h 05m".
func Until(now, t time.Time) string {
	d := t.Sub(now)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
