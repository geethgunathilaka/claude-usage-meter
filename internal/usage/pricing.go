package usage

import "strings"

// Price is the API list price of a model in US dollars per million tokens.
type Price struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheWrite5m float64 `json:"cacheWrite5m"`
	CacheWrite1h float64 `json:"cacheWrite1h"`
	CacheRead    float64 `json:"cacheRead"`
}

// PriceTable maps a model id (or id prefix) to its price.
// Lookup uses the longest matching prefix, so "claude-opus-4" covers every 4.x Opus
// that does not have its own entry.
type PriceTable map[string]Price

func p(in, out, read float64) Price {
	// Cache writes follow the standard multipliers: 5-minute TTL = 1.25x input, 1-hour TTL = 2x input.
	return Price{Input: in, Output: out, CacheWrite5m: in * 1.25, CacheWrite1h: in * 2, CacheRead: read}
}

// DefaultPrices returns first-party Claude API list prices (USD per 1M tokens).
// They are estimates for display only; override any of them in config.json.
func DefaultPrices() PriceTable {
	return PriceTable{
		"claude-fable-5-2":  p(8, 40, 0.20),
		"claude-mythos-5-2": p(8, 40, 0.20),
		"claude-fable-5-1":  p(10, 50, 1.00),
		"claude-mythos-5-1": p(10, 50, 1.00),
		"claude-fable-5":    p(10, 50, 1.00),
		"claude-mythos-5":   p(10, 50, 1.00),
		"claude-opus-5-5":   p(4, 20, 0.20),
		"claude-opus-5":     p(5, 25, 0.50),
		"claude-opus-4":     p(5, 25, 0.50),
		"claude-sonnet-5-5": p(2, 10, 0.10),
		"claude-sonnet-5":   p(2, 10, 0.20),
		"claude-sonnet-4":   p(3, 15, 0.30),
		"claude-haiku-5-5":  p(0.10, 0.50, 0.01),
		"claude-haiku-4":    p(1, 5, 0.10),
	}
}

// Merge returns a copy of t with every entry of override applied on top.
func (t PriceTable) Merge(override PriceTable) PriceTable {
	out := PriceTable{}
	for k, v := range t {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// For returns the price for a model id using the longest matching key prefix.
// If nothing matches it falls back to the model family (fable/mythos, opus, sonnet, haiku).
func (t PriceTable) For(model string) (Price, bool) {
	m := strings.ToLower(model)
	best, bestLen := Price{}, -1
	for k, v := range t {
		if strings.HasPrefix(m, strings.ToLower(k)) && len(k) > bestLen {
			best, bestLen = v, len(k)
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	for _, fam := range []string{"fable", "mythos", "opus", "sonnet", "haiku"} {
		if strings.Contains(m, fam) {
			// pick the most recent family entry we know about
			for _, k := range []string{"claude-" + fam + "-5-5", "claude-" + fam + "-5-2", "claude-" + fam + "-5", "claude-" + fam + "-4"} {
				if v, ok := t[k]; ok {
					return v, true
				}
			}
		}
	}
	return Price{}, false
}

// Cost returns the estimated API-equivalent cost of one entry in US dollars.
func (pr Price) Cost(e Entry) float64 {
	return (float64(e.Input)*pr.Input +
		float64(e.Output)*pr.Output +
		float64(e.CacheWrite5m)*pr.CacheWrite5m +
		float64(e.CacheWrite1h)*pr.CacheWrite1h +
		float64(e.CacheRead)*pr.CacheRead) / 1_000_000
}
