//go:build windows

// Claude Usage Meter: a Windows tray app that shows your Claude plan usage
// (Session 5h, Weekly, per-model weekly) and your Claude Code token totals, with
// an always-on-top usage bar and pop-up alerts every 25%.
//
// Two sources:
//   - Official: claude.ai's own usage endpoint, when a claude.ai session key is set in Settings.
//     These are the same numbers claude.ai and Claude Code show.
//   - Local estimate: Claude Code's log files on this PC. No network, no key.
package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/systray"

	"claude-usage-meter/internal/alerts"
	"claude-usage-meter/internal/config"
	"claude-usage-meter/internal/format"
	"claude-usage-meter/internal/official"
	"claude-usage-meter/internal/trayicon"
	"claude-usage-meter/internal/ui"
	"claude-usage-meter/internal/usage"
)

var version = "dev" // set at build time with -ldflags "-X main.version=..."

func main() {
	if !singleInstance() {
		return
	}
	enableDPIAwareness()
	systray.Run(onReady, func() {})
}

const maxBars = 5

type menu struct {
	planHeader *systray.MenuItem
	bars       [maxBars]*systray.MenuItem
	source     *systray.MenuItem

	localHeader, window, resets, burn *systray.MenuItem
	today, week, model                *systray.MenuItem

	widget, onTop, alerts, testAlert *systray.MenuItem
	styles                           map[string]*systray.MenuItem
	positions                        map[string]*systray.MenuItem
	refresh, settings, logs          *systray.MenuItem
	autostart, about, quit           *systray.MenuItem
}

var (
	styleNames = []struct{ key, title string }{{"compact", "Compact (rings)"}, {"bar", "Full bar"}}
	posNames   = []struct{ key, title string }{
		{"top-left", "Top left"}, {"top-center", "Top centre"}, {"top-right", "Top right"},
		{"bottom-left", "Bottom left"}, {"bottom-right", "Bottom right"},
	}
)

// syncChecks ticks the style and position entries that match the settings.
func (m *menu) syncChecks(cfg config.Config) {
	for k, it := range m.styles {
		if k == cfg.WidgetStyle {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	for k, it := range m.positions {
		if k == cfg.WidgetPosition {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	setChecked(m.widget, cfg.WidgetEnabled)
	setChecked(m.onTop, cfg.WidgetOnTop)
	setChecked(m.alerts, cfg.AlertsEnabled)
}

func setChecked(it *systray.MenuItem, on bool) {
	if on {
		it.Check()
	} else {
		it.Uncheck()
	}
}

func disabled(title string) *systray.MenuItem {
	it := systray.AddMenuItem(title, "")
	it.Disable()
	return it
}

func onReady() {
	systray.SetIcon(trayicon.Render(-1, trayicon.Unknown))
	systray.SetTitle("Claude Usage")
	systray.SetTooltip("Claude usage: loading…")
	cfg, _ := config.Load()

	m := &menu{}
	m.planHeader = disabled("Claude plan usage")
	for i := range m.bars {
		m.bars[i] = disabled("")
		m.bars[i].Hide()
	}
	m.source = disabled("Loading…")
	systray.AddSeparator()
	m.localHeader = disabled("Claude Code on this PC (estimate)")
	m.window = disabled("Reading Claude Code logs…")
	m.resets = disabled("")
	m.burn = disabled("")
	m.today = disabled("")
	m.week = disabled("")
	m.model = disabled("")
	systray.AddSeparator()
	m.widget = systray.AddMenuItemCheckbox("Show usage meter on desktop", "Sits on the desktop behind your apps; drag it anywhere", cfg.WidgetEnabled)
	m.onTop = systray.AddMenuItemCheckbox("Keep on top of apps (see-through, click-through)", "Floats over your apps without blocking clicks", cfg.WidgetOnTop)
	styleMenu := systray.AddMenuItem("Meter style", "")
	m.styles = map[string]*systray.MenuItem{}
	for _, s := range styleNames {
		m.styles[s.key] = styleMenu.AddSubMenuItemCheckbox(s.title, "", cfg.WidgetStyle == s.key)
	}
	posMenu := systray.AddMenuItem("Meter position", "Or drag the meter (when it is not on top)")
	m.positions = map[string]*systray.MenuItem{}
	for _, p := range posNames {
		m.positions[p.key] = posMenu.AddSubMenuItemCheckbox(p.title, "", cfg.WidgetPosition == p.key)
	}
	m.alerts = systray.AddMenuItemCheckbox("Pop-up alerts at "+stepsLabel(cfg.AlertSteps), "Change the steps in Settings (alertSteps)", cfg.AlertsEnabled)
	m.testAlert = systray.AddMenuItem("Show a test alert", "")
	systray.AddSeparator()
	m.refresh = systray.AddMenuItem("Refresh now", "")
	m.settings = systray.AddMenuItem("Settings…", "Open config.json: claude.ai session key, alerts, limits, prices")
	m.logs = systray.AddMenuItem("Open Claude logs folder", "")
	m.autostart = systray.AddMenuItemCheckbox("Start with Windows", "", autostartEnabled())
	systray.AddSeparator()
	m.about = disabled("Claude Usage Meter " + version + " · not affiliated with Anthropic")
	m.quit = systray.AddMenuItem("Quit", "")

	a := &app{menu: m, kick: make(chan struct{}, 1), alertState: alerts.Load(config.StatePath())}
	a.overlay = startOverlay(func(x, y int) {
		if c, err := config.Load(); err == nil {
			c.WidgetPosition, c.WidgetX, c.WidgetY = "custom", x, y
			_ = config.Save(c)
			m.syncChecks(c)
		}
	})
	for key, it := range m.styles {
		go a.onClick(it, func(c *config.Config) { c.WidgetStyle = key; c.WidgetEnabled = true })
	}
	for key, it := range m.positions {
		go a.onClick(it, func(c *config.Config) { c.WidgetPosition = key; c.WidgetEnabled = true })
	}
	go a.onClick(m.widget, func(c *config.Config) {
		c.WidgetEnabled = !c.WidgetEnabled
		if !c.WidgetEnabled {
			c.WidgetOnTop = false
		}
	})
	// turning "on top" on also turns the meter on
	go a.onClick(m.onTop, func(c *config.Config) {
		c.WidgetOnTop = !c.WidgetOnTop
		if c.WidgetOnTop {
			c.WidgetEnabled = true
		}
	})
	go a.onClick(m.alerts, func(c *config.Config) { c.AlertsEnabled = !c.AlertsEnabled })
	go a.loop()
	go a.handleClicks()
}

// onClick applies change to the settings each time the item is clicked, then refreshes.
func (a *app) onClick(it *systray.MenuItem, change func(c *config.Config)) {
	for range it.ClickedCh {
		c, err := config.Load()
		if err != nil {
			continue
		}
		change(&c)
		_ = config.Save(c)
		a.menu.syncChecks(c)
		a.refreshNow()
	}
}

func stepsLabel(steps []float64) string {
	var p []string
	for _, s := range steps {
		p = append(p, fmt.Sprintf("%.0f", s))
	}
	return strings.Join(p, " / ") + "%"
}

type app struct {
	menu    *menu
	kick    chan struct{}
	overlay *overlay
	scanner *usage.Scanner
	scanKey string

	client      *official.Client
	clientKey   string
	lastPlan    *official.Usage
	lastPlanAt  time.Time
	lastPlanErr error
	nextPlanAt  time.Time

	alertState alerts.State
	lastBars   []alerts.Limit // what the widget currently shows, for the test alert
}

func (a *app) refreshNow() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *app) loop() {
	for {
		cfg := a.update()
		select {
		case <-time.After(time.Duration(cfg.RefreshSeconds) * time.Second):
		case <-a.kick:
			a.nextPlanAt = time.Time{} // manual refresh also re-fetches the official numbers
		}
	}
}

func toggleSetting(set func(c *config.Config, on bool), on bool) {
	if c, err := config.Load(); err == nil {
		set(&c, on)
		_ = config.Save(c)
	}
}

func (a *app) handleClicks() {
	m := a.menu
	for {
		select {
		case <-m.refresh.ClickedCh:
			a.refreshNow()
		case <-m.settings.ClickedCh:
			_, _ = config.Load() // make sure the file exists
			openText(config.Path())
		case <-m.logs.ClickedCh:
			for _, d := range usage.DefaultDirs() {
				p := filepath.Join(d, "projects")
				if _, err := os.Stat(p); err == nil {
					open(p)
					break
				}
			}
		case <-m.testAlert.ClickedCh:
			cfg, _ := config.Load()
			l := alerts.Limit{Name: "Session (5hr)", Percent: 75}
			if len(a.lastBars) > 0 {
				l = a.lastBars[0]
			}
			a.showAlerts(cfg, []alerts.Alert{{Limit: l, Step: l.Percent}}, a.lastBars, true)
		case <-m.autostart.ClickedCh:
			want := !m.autostart.Checked()
			if err := setAutostart(want); err == nil {
				if want {
					m.autostart.Check()
				} else {
					m.autostart.Uncheck()
				}
			}
		case <-m.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func levelFor(pct float64, cfg config.Config) trayicon.Level {
	switch {
	case pct < 0:
		return trayicon.Unknown
	case pct >= cfg.AlertPercent:
		return trayicon.Alert
	case pct >= cfg.WarnPercent:
		return trayicon.Warn
	}
	return trayicon.OK
}

func uiLevel(pct float64, cfg config.Config) ui.Level {
	switch {
	case pct >= cfg.AlertPercent:
		return ui.Alert
	case pct >= cfg.WarnPercent:
		return ui.Warn
	}
	return ui.OK
}

func resetLabel(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt := t.Local()
	if lt.Sub(now) < 20*time.Hour {
		return fmt.Sprintf(" · resets %s (in %s)", lt.Format("15:04"), format.Until(now, t))
	}
	return " · resets " + lt.Format("Mon 2 Jan 15:04")
}

func shortResets(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt := t.Local()
	if lt.Sub(now) < 20*time.Hour {
		return lt.Format("15:04")
	}
	return lt.Format("Mon 15:04")
}

// fetchPlan refreshes the official numbers at most once a minute.
func (a *app) fetchPlan(cfg config.Config, now time.Time) {
	if cfg.ClaudeAISessionKey == "" {
		a.lastPlan, a.lastPlanErr = nil, nil
		return
	}
	key := cfg.ClaudeAISessionKey + "|" + cfg.OrganizationID
	if a.client == nil || key != a.clientKey {
		a.client = &official.Client{SessionKey: cfg.ClaudeAISessionKey, OrgID: cfg.OrganizationID}
		a.clientKey = key
		a.nextPlanAt = time.Time{}
	}
	if now.Before(a.nextPlanAt) {
		return
	}
	interval := time.Duration(cfg.RefreshSeconds) * time.Second
	if interval < time.Minute {
		interval = time.Minute
	}
	a.nextPlanAt = now.Add(interval)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	u, err := a.client.Fetch(ctx)
	a.lastPlanErr = err
	if err == nil {
		a.lastPlan, a.lastPlanAt = &u, now
	}
}

func barLabel(name string) string {
	switch name {
	case "Session (5hr)":
		return "5h"
	case "Weekly (7 day)":
		return "Week"
	}
	return strings.TrimPrefix(name, "Weekly ")
}

// showAlerts pops up one card covering every new crossing (the fullest first).
func (a *app) showAlerts(cfg config.Config, fired []alerts.Alert, all []alerts.Limit, test bool) {
	if len(fired) == 0 {
		return
	}
	top := fired[0]
	for _, f := range fired {
		if f.Limit.Percent > top.Limit.Percent {
			top = f
		}
	}
	now := time.Now()
	title := fmt.Sprintf("%s passed %.0f%%", top.Limit.Name, top.Step)
	if top.Step >= 100 {
		title = top.Limit.Name + " limit reached"
	}
	if test {
		title = "Test alert · " + title
	}
	var parts []string
	if !top.Limit.ResetsAt.IsZero() {
		lt := top.Limit.ResetsAt.Local()
		if lt.Sub(now) < 20*time.Hour {
			parts = append(parts, fmt.Sprintf("Resets %s (in %s).", lt.Format("15:04"), format.Until(now, top.Limit.ResetsAt)))
		} else {
			parts = append(parts, "Resets "+lt.Format("Mon 2 Jan 15:04")+".")
		}
	}
	var others []string
	for _, l := range all {
		if l.Name != top.Limit.Name {
			others = append(others, fmt.Sprintf("%s %s", barLabel(l.Name), format.Percent(l.Percent)))
		}
	}
	if len(others) > 0 {
		parts = append(parts, strings.Join(others, " · "))
	}
	img := ui.RenderPopup(ui.PopupData{Title: title, Body: strings.Join(parts, " "), Percent: top.Limit.Percent, Level: uiLevel(top.Limit.Percent, cfg)}, a.overlay.scale)
	a.overlay.ShowPopup(img, time.Duration(cfg.AlertSeconds)*time.Second)
	if cfg.AlertSound {
		beep()
	}
}

// update re-reads the settings, the official usage and the local logs, and refreshes the tray,
// the desktop bar and the alerts.
func (a *app) update() config.Config {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	now := time.Now()
	m := a.menu

	// ---- local logs (always) ----
	key := fmt.Sprint(cfg.Dirs(), cfg.ScanDays, cfg.Prices)
	if a.scanner == nil || key != a.scanKey {
		a.scanner = usage.NewScanner(cfg.Dirs(), cfg.MaxAge(), cfg.PriceTable())
		a.scanKey = key
	}
	var local *usage.Summary
	if err := a.scanner.Refresh(now); err == nil {
		s := usage.Summarize(a.scanner.Entries(), now, cfg.BlockTokenLimit)
		local = &s
	}

	// ---- official plan usage (when a session key is set) ----
	a.fetchPlan(cfg, now)
	plan := a.lastPlan
	if plan != nil && now.Sub(a.lastPlanAt) > 15*time.Minute {
		plan = nil // too old to trust
	}

	// the limits shown on the bar and watched by alerts
	var watched []alerts.Limit
	widget := ui.WidgetData{}

	for _, b := range m.bars {
		b.Hide()
	}
	switch {
	case plan != nil:
		m.planHeader.SetTitle("Claude plan usage (from claude.ai)")
		for i, l := range plan.All() {
			if i < maxBars {
				m.bars[i].SetTitle(fmt.Sprintf("%s   %s%s", l.Name, format.Percent(l.Percent), resetLabel(now, l.ResetsAt)))
				m.bars[i].Show()
			}
			watched = append(watched, alerts.Limit{Name: l.Name, Percent: l.Percent, ResetsAt: l.ResetsAt})
			widget.Bars = append(widget.Bars, ui.Bar{Label: barLabel(l.Name), Percent: l.Percent, Level: uiLevel(l.Percent, cfg)})
		}
		src := "Live · updated " + a.lastPlanAt.Format("15:04")
		if a.lastPlanErr != nil {
			src += " · last refresh failed: " + a.lastPlanErr.Error()
		}
		m.source.SetTitle(src)
		if plan.Session != nil && !plan.Session.ResetsAt.IsZero() {
			widget.ResetTime = shortResets(now, plan.Session.ResetsAt)
			widget.ResetIn = "in " + format.Until(now, plan.Session.ResetsAt)
		}

		ring := -1.0
		if plan.Session != nil {
			ring = plan.Session.Percent
		} else if h := plan.Highest(); h != nil {
			ring = h.Percent
		}
		worst := plan.Highest().Percent // colour follows the fullest bar, so a full weekly limit shows red
		systray.SetIcon(trayicon.Render(ring, levelFor(worst, cfg)))
		tip := "Claude"
		for _, l := range plan.All() {
			tip += fmt.Sprintf(" · %s %s", barLabel(l.Name), format.Percent(l.Percent))
		}
		systray.SetTooltip(trim(tip, 120))

	default:
		m.planHeader.SetTitle("Claude plan usage")
		switch {
		case cfg.ClaudeAISessionKey == "":
			m.source.SetTitle("For exact claude.ai figures, add your session key in Settings")
		case a.lastPlanErr != nil:
			m.source.SetTitle("claude.ai: " + a.lastPlanErr.Error())
		default:
			m.source.SetTitle("claude.ai: no recent data")
		}
		if local != nil && local.Percent >= 0 {
			systray.SetIcon(trayicon.Render(local.Percent, levelFor(local.Percent, cfg)))
			systray.SetTooltip("Claude (estimate) · 5h window " + format.Percent(local.Percent))
			widget.Bars = []ui.Bar{{Label: "5h est.", Percent: local.Percent, Level: uiLevel(local.Percent, cfg)}}
			if local.Active != nil {
				widget.ResetTime = local.Active.End.Local().Format("15:04")
				widget.ResetIn = "in " + format.Until(now, local.Active.End)
				watched = []alerts.Limit{{Name: "Session (estimate)", Percent: local.Percent, ResetsAt: local.Active.End}}
			}
		} else {
			systray.SetIcon(trayicon.Render(-1, trayicon.Unknown))
			systray.SetTooltip("Claude usage: no data")
			widget.Message = "Claude usage: add your claude.ai session key in Settings"
		}
	}

	// ---- desktop bar ----
	a.lastBars = watched
	if cfg.WidgetEnabled {
		var img *image.RGBA
		if cfg.WidgetStyle == "bar" {
			img = ui.RenderWidget(widget, a.overlay.scale)
		} else {
			img = ui.RenderCompact(widget, a.overlay.scale)
		}
		a.overlay.ShowWidget(img, WidgetOptions{Position: cfg.WidgetPosition, X: cfg.WidgetX, Y: cfg.WidgetY, OnTop: cfg.WidgetOnTop, Opacity: cfg.WidgetOpacity})
	} else {
		a.overlay.HideWidget()
	}

	// ---- alerts every 25% ----
	if len(watched) > 0 {
		fired := a.alertState.Check(watched, cfg.AlertSteps)
		_ = a.alertState.Save(config.StatePath())
		if cfg.AlertsEnabled {
			a.showAlerts(cfg, fired, watched, false)
		}
	}
	m.alerts.SetTitle("Pop-up alerts at " + stepsLabel(cfg.AlertSteps))

	// ---- local section ----
	if local == nil {
		m.window.SetTitle("No Claude Code logs found on this PC")
		for _, it := range []*systray.MenuItem{m.resets, m.burn, m.today, m.week, m.model} {
			it.SetTitle("")
		}
		return cfg
	}
	s := local
	if b := s.Active; b != nil {
		note := ""
		if plan == nil {
			note = format.Percent(s.Percent) + " est. · "
		}
		m.window.SetTitle(fmt.Sprintf("This 5h window: %s%s tokens · %s", note, format.Tokens(b.Tokens), format.Money(b.Cost)))
		m.resets.SetTitle(fmt.Sprintf("Window resets %s (in %s)", b.End.Local().Format("15:04"), format.Until(now, b.End)))
		m.burn.SetTitle(fmt.Sprintf("Burn rate %s tokens/min · %s cache reads", format.Tokens(int64(s.BurnPerMin)), format.Tokens(b.CacheRead)))
	} else {
		m.window.SetTitle("Claude Code idle: no active 5-hour window")
		m.resets.SetTitle("")
		m.burn.SetTitle("")
	}
	m.today.SetTitle(fmt.Sprintf("Today: %s tokens · %s API-equivalent", format.Tokens(s.Today.Tokens), format.Money(s.Today.Cost)))
	m.week.SetTitle(fmt.Sprintf("Last 7 days: %s tokens · %s", format.Tokens(s.Week.Tokens), format.Money(s.Week.Cost)))
	if tm := s.Today.TopModel(); tm != "" {
		m.model.SetTitle("Top model today: " + tm)
	} else {
		m.model.SetTitle("Top model this week: " + s.Week.TopModel())
	}
	return cfg
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
