// claude-usage-report prints the same numbers the tray shows, in the console.
// Useful for checking the meter and for scripting.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"claude-usage-meter/internal/config"
	"claude-usage-meter/internal/format"
	"claude-usage-meter/internal/official"
	"claude-usage-meter/internal/usage"
)

func main() {
	asJSON := flag.Bool("json", false, "print the summary as JSON")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "settings:", err)
	}
	sc := usage.NewScanner(cfg.Dirs(), cfg.MaxAge(), cfg.PriceTable())
	start := time.Now()
	if err := sc.Refresh(time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := usage.Summarize(sc.Entries(), time.Now(), cfg.BlockTokenLimit)

	var plan *official.Usage
	var planErr error
	if cfg.ClaudeAISessionKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		c := &official.Client{SessionKey: cfg.ClaudeAISessionKey, OrgID: cfg.OrganizationID}
		u, err := c.Fetch(ctx)
		cancel()
		if err == nil {
			plan = &u
		}
		planErr = err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"plan": plan, "local": s})
		return
	}
	switch {
	case plan != nil:
		fmt.Println("Claude plan usage (from claude.ai)")
		for _, l := range plan.All() {
			reset := ""
			if !l.ResetsAt.IsZero() {
				reset = "  resets " + l.ResetsAt.Local().Format("Mon 2 Jan 15:04")
			}
			fmt.Printf("  %-16s %4s%s\n", l.Name, format.Percent(l.Percent), reset)
		}
		fmt.Println()
	case planErr != nil:
		fmt.Printf("claude.ai plan usage unavailable: %v\n\n", planErr)
	default:
		fmt.Printf("claude.ai plan usage: add claudeAiSessionKey to %s for exact figures.\n\n", config.Path())
	}
	fmt.Println("Claude Code on this PC (estimate from local logs)")
	fmt.Printf("Scanned %d responses from the last %d days in %s\n\n", len(sc.Entries()), cfg.ScanDays, time.Since(start).Round(time.Millisecond))
	if s.Active != nil {
		b := s.Active
		fmt.Printf("Current 5-hour window  %s used", format.Percent(s.Percent))
		if s.AutoLimit {
			fmt.Printf(" (of your largest window, %s tokens)", format.Tokens(s.Limit))
		} else if s.Limit > 0 {
			fmt.Printf(" (of %s tokens)", format.Tokens(s.Limit))
		}
		fmt.Println()
		fmt.Printf("  Tokens   %s  (+%s cache reads)\n", format.Tokens(b.Tokens), format.Tokens(b.CacheRead))
		fmt.Printf("  Cost     %s (API-equivalent estimate)\n", format.Money(b.Cost))
		fmt.Printf("  Resets   %s (in %s)\n", b.End.Local().Format("15:04"), format.Until(s.Now, b.End))
		fmt.Printf("  Burn     %s tokens/min\n\n", format.Tokens(int64(s.BurnPerMin)))
	} else {
		fmt.Println("No active 5-hour window (idle).")
		fmt.Println()
	}
	fmt.Printf("Today        %s tokens  %s  %d requests  top model: %s\n", format.Tokens(s.Today.Tokens), format.Money(s.Today.Cost), s.Today.Requests, s.Today.TopModel())
	fmt.Printf("Last 7 days  %s tokens  %s  %d requests  top model: %s\n", format.Tokens(s.Week.Tokens), format.Money(s.Week.Cost), s.Week.Requests, s.Week.TopModel())
	fmt.Printf("Largest 5-hour window in range: %s tokens\n", format.Tokens(s.PeakBlock))
	fmt.Printf("\nSettings: %s\n", config.Path())
}
