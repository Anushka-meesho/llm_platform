package tasks

// SeedPlayground registers the built-in playground task (free-form prompt, no
// schemas) that the Compare UI's /run endpoint attributes its usage to.
// Idempotent: an existing playground task is left untouched so operators can
// tweak it (e.g. budget) without the seed reverting their changes.
//
// This is the only task seeded at startup. Product tasks live exclusively in
// the DB and are authored at runtime through the Studio (POST /v1/tasks) — there
// is no YAML/file seeding layer.
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
