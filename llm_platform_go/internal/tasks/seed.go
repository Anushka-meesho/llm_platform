package tasks

import "encoding/json"

// SeedPlayground registers the built-in playground task (free-form prompt, no
// schemas) that the Compare UI's /run endpoint attributes its usage to.
// Idempotent: an existing task is left untouched so operators can tweak it
// (e.g. budget) without the seed reverting their changes.
func SeedPlayground(store *Store) error {
	if _, err := store.Get(PlaygroundTaskID); err == nil {
		return nil
	} else if err != ErrNotFound {
		return err
	}
	return store.Create(&Task{
		ID:             PlaygroundTaskID,
		Name:           "Playground",
		Description:    "Built-in multi-model comparison playground (the Compare UI). Free-form prompts, no schemas.",
		PromptTemplate: "{{.prompt}}", // not used by /run, present to satisfy validation
		Model:          "gpt-4o-mini",
		Temperature:    0.7,
		MaxTokens:      1000,
	})
}

// SeedAttributeExtraction registers the attribute-extraction task.
// Idempotent: returns early if the task already exists.
func SeedAttributeExtraction(store *Store) error {
	const id = "attribute-extraction"
	if _, err := store.Get(id); err == nil {
		return nil
	} else if err != ErrNotFound {
		return err
	}
	return store.Create(&Task{
		ID:          id,
		Name:        "Attribute Extraction",
		Description: "Extract structured product attributes from a seller listing — title, category, optional description/brand, and optional product photo. First real task on the platform (CIS prefill shadow target).",
		Model:          "gemini-2.5-flash",
		FallbackModels: []string{"gpt-4o-mini"},
		Temperature:    0.1,
		MaxTokens:      2048,
		DailyBudgetUSD: 50,
		SystemPrompt: `You are a product attribute extraction engine for an e-commerce catalog. You may receive a product image in addition to the text fields; when an image is present, read attributes visible in it (colour, pattern, material, sleeve length, neckline, shape, etc.) and combine them with the text. Respond with JSON only — no prose, no markdown fences.`,
		PromptTemplate: `Extract product attributes for this catalog listing.

Title: {{.title}}
{{if .description}}Description: {{.description}}{{end}}
Category: {{.category}}
{{if .brand}}Brand: {{.brand}}{{end}}
{{if .image}}A product photo is attached — extract attributes visible in it as well as from the text.{{end}}

Return a JSON object with exactly two keys:
- "attributes": an object of attribute name -> value (strings only, e.g. "color": "red", "material": "cotton")
- "confidence": a number between 0 and 1 for your overall confidence

Extract only attributes clearly supported by the listing text or image. Respond with JSON only.`,
		InputSchema: json.RawMessage(`{"type":"object","required":["title","category"],"additionalProperties":false,"properties":{"title":{"type":"string","minLength":3,"description":"Product title / name as it appears on the listing"},"description":{"type":"string","description":"Optional seller-written product description"},"category":{"type":"string","minLength":2,"description":"Product category, e.g. \"Men's T-Shirts\" or \"Kitchen Storage\""},"brand":{"type":"string","description":"Brand name, if the seller knows it"},"image":{"type":"string","description":"Optional product photo as a base64 data URL (\"data:image/jpeg;base64,...\") or an image URL."}}}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["attributes","confidence"],"properties":{"attributes":{"type":"object","additionalProperties":{"type":"string"}},"confidence":{"type":"number","minimum":0,"maximum":1}}}`),
		CacheEnabled:    true,
		CacheTTLSeconds: 86400,
	})
}
