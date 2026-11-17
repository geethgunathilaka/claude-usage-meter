// Package config loads and saves the meter's settings file.
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"claude-usage-meter/internal/usage"
)

// Config is stored as JSON in %APPDATA%\ClaudeUsageMeter\config.json.
type Config struct {
	// RefreshSeconds is how often the logs are re-read.
	RefreshSeconds int `json:"refreshSeconds"`
	// BlockTokenLimit is the token budget for one 5-hour window used for the percentage.
	// 0 means "auto": the largest window seen in the last ScanDays days.
	BlockTokenLimit int64 `json:"blockTokenLimit"`
	// ScanDays is how far back logs are read.
	ScanDays int `json:"scanDays"`
	// WarnPercent and AlertPercent switch the tray icon to amber and red.
	WarnPercent  float64 `json:"warnPercent"`
	AlertPercent float64 `json:"alertPercent"`
	// ClaudeDirs overrides where Claude Code logs are read from (folders that contain "projects").
	// Empty means CLAUDE_CONFIG_DIR, or ~/.claude and ~/.config/claude.
	ClaudeDirs []string `json:"claudeDirs"`
	// Prices overrides or adds model prices in USD per 1M tokens. Keys are model ids or id prefixes.
	Prices usage.PriceTable `json:"prices,omitempty"`

	// ClaudeAISessionKey is the value of the "sessionKey" cookie on claude.ai. When set, the meter
	// shows the official plan usage (Session 5h, Weekly, per-model weekly) exactly as claude.ai does.
	// Treat it like a password: it gives full access to your claude.ai account.
	ClaudeAISessionKey string `json:"claudeAiSessionKey"`
	// OrganizationID is optional; when empty the first chat organization on the account is used.
	OrganizationID string `json:"organizationId"`

	// WidgetEnabled shows the usage meter on the desktop.
	WidgetEnabled bool `json:"widgetEnabled"`
	// WidgetStyle is "compact" (small card with rings) or "bar" (wide bar with labels).
	WidgetStyle string `json:"widgetStyle"`
	// WidgetPosition is "top-left", "top-right", "top-center", "bottom-left", "bottom-right",
	// or "custom" (where it was dragged to: WidgetX / WidgetY).
	WidgetPosition string `json:"widgetPosition"`
	WidgetX        int    `json:"widgetX"`
	WidgetY        int    `json:"widgetY"`
	// WidgetOnTop keeps the meter above other apps. It is then click-through (clicks go to the
	// window underneath) and drawn at WidgetOpacity so it does not get in the way.
	WidgetOnTop   bool    `json:"widgetOnTop"`
	WidgetOpacity float64 `json:"widgetOpacity"`

	// AlertsEnabled shows a pop-up when a limit crosses one of AlertSteps (percent).
	AlertsEnabled bool      `json:"alertsEnabled"`
	AlertSteps    []float64 `json:"alertSteps"`
	// AlertSound plays the Windows warning sound with each pop-up.
	AlertSound bool `json:"alertSound"`
	// AlertSeconds is how long a pop-up stays on screen.
	AlertSeconds int `json:"alertSeconds"`
}

// Default returns the default settings.
func Default() Config {
	return Config{RefreshSeconds: 60, BlockTokenLimit: 0, ScanDays: 8, WarnPercent: 75, AlertPercent: 90,
		WidgetEnabled: true, WidgetStyle: "compact", WidgetPosition: "top-right", WidgetX: -1, WidgetY: -1,
		WidgetOnTop: false, WidgetOpacity: 0.85,
		AlertsEnabled: true, AlertSteps: []float64{25, 50, 75, 100}, AlertSound: true, AlertSeconds: 12}
}

// StatePath is where alert history is kept.
func StatePath() string { return filepath.Join(filepath.Dir(Path()), "alerts-state.json") }

// Path returns the settings file location.
func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "ClaudeUsageMeter", "config.json")
}

// Load reads the settings file, creating it with defaults if it does not exist.
// Missing or invalid values fall back to the defaults.
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return c, Save(c)
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Default(), err
	}
	d := Default()
	if c.RefreshSeconds < 10 {
		c.RefreshSeconds = d.RefreshSeconds
	}
	if c.ScanDays < 1 {
		c.ScanDays = d.ScanDays
	}
	if c.WarnPercent <= 0 {
		c.WarnPercent = d.WarnPercent
	}
	if c.AlertPercent <= 0 {
		c.AlertPercent = d.AlertPercent
	}
	if len(c.AlertSteps) == 0 {
		c.AlertSteps = d.AlertSteps
	}
	if c.WidgetStyle != "bar" && c.WidgetStyle != "compact" {
		c.WidgetStyle = d.WidgetStyle
	}
	switch c.WidgetPosition {
	case "top-left", "top-right", "top-center", "bottom-left", "bottom-right", "custom":
	default:
		c.WidgetPosition = d.WidgetPosition
	}
	if c.WidgetOpacity < 0.3 || c.WidgetOpacity > 1 {
		c.WidgetOpacity = d.WidgetOpacity
	}
	if c.AlertSeconds < 3 {
		c.AlertSeconds = d.AlertSeconds
	}
	// Files written by an older version lack newer settings; rewrite so they appear in the file.
	if !bytes.Contains(b, []byte(`"claudeAiSessionKey"`)) || !bytes.Contains(b, []byte(`"alertSteps"`)) || !bytes.Contains(b, []byte(`"widgetOnTop"`)) {
		_ = Save(c)
	}
	return c, nil
}

// Save writes the settings file.
func Save(c Config) error {
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(Path(), b, 0o644)
}

// Dirs returns the Claude directories to scan.
func (c Config) Dirs() []string {
	if len(c.ClaudeDirs) > 0 {
		return c.ClaudeDirs
	}
	return usage.DefaultDirs()
}

// PriceTable returns the default prices with the user's overrides applied.
func (c Config) PriceTable() usage.PriceTable { return usage.DefaultPrices().Merge(c.Prices) }

// MaxAge is the scan window as a duration.
func (c Config) MaxAge() time.Duration { return time.Duration(c.ScanDays) * 24 * time.Hour }
