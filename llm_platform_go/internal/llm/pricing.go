package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Rate holds per-provider token pricing. Exported so tests can inject directly.
type Rate struct {
	InputPer1M      float64 `json:"input_per_1m"`
	OutputPer1M     float64 `json:"output_per_1m"`
	ContextWindow   int     `json:"context_window"`
	MaxOutputTokens int     `json:"max_output_tokens"`
}

var pricingTable map[string]Rate

// LoadPricing reads pricing.json into memory. Called once at startup.
func LoadPricing(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pricing file %q: %w", path, err)
	}
	if err := json.Unmarshal(data, &pricingTable); err != nil {
		return fmt.Errorf("parse pricing file: %w", err)
	}
	return nil
}

// LoadPricingFromMap allows tests to inject pricing without a file on disk.
func LoadPricingFromMap(m map[string]Rate) error {
	pricingTable = m
	return nil
}

// GetMaxOutputTokens returns the model's maximum allowed output tokens.
// Returns 0 for unknown models.
func GetMaxOutputTokens(model string) int {
	r, ok := pricingTable[model]
	if !ok {
		return 0
	}
	return r.MaxOutputTokens
}

// GetContextWindow returns the model's maximum context window in tokens.
// Returns 0 for unknown models.
func GetContextWindow(model string) int {
	r, ok := pricingTable[model]
	if !ok {
		return 0
	}
	return r.ContextWindow
}

// GetAllRates returns a copy of the full pricing table (model name → Rate).
func GetAllRates() map[string]Rate {
	out := make(map[string]Rate, len(pricingTable))
	for k, v := range pricingTable {
		out[k] = v
	}
	return out
}

// CalculateCost returns the estimated USD cost for one model call.
// Returns 0.0 for unknown models (no panic, just free).
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	r, ok := pricingTable[model]
	if !ok {
		return 0.0
	}
	cost := (float64(inputTokens)/1_000_000)*r.InputPer1M +
		(float64(outputTokens)/1_000_000)*r.OutputPer1M
	// Round to 6 decimal places to match Python behaviour.
	return math.Round(cost*1_000_000) / 1_000_000
}
