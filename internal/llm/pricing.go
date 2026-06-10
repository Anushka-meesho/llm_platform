package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Rate holds per-provider token pricing. Exported so tests can inject directly.
type Rate struct {
	InputPer1M  float64 `json:"input_per_1m"`
	OutputPer1M float64 `json:"output_per_1m"`
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
