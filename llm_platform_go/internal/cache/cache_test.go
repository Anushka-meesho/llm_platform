package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func baseInputs() KeyInputs {
	return KeyInputs{
		TaskID:         "attribute-extraction",
		PromptVersion:  4,
		Model:          "llama-groq",
		SystemPrompt:   "You are an extractor.",
		RenderedPrompt: "Extract attributes from: Blue Cotton Kurti",
		Temperature:    0.2,
		MaxTokens:      1000,
		OutputSchema:   `{"type":"object"}`,
	}
}

func TestKeyDeterministicAndSensitive(t *testing.T) {
	a, b := baseInputs(), baseInputs()
	if Key(a) != Key(b) {
		t.Fatal("identical inputs must produce identical keys")
	}

	// Every field that determines the model output must change the key.
	mutations := map[string]func(*KeyInputs){
		"task":     func(k *KeyInputs) { k.TaskID = "other-task" },
		"version":  func(k *KeyInputs) { k.PromptVersion = 5 },
		"model":    func(k *KeyInputs) { k.Model = "gpt-4o-mini" },
		"system":   func(k *KeyInputs) { k.SystemPrompt = "different" },
		"rendered": func(k *KeyInputs) { k.RenderedPrompt = "Extract attributes from: Red Saree" },
		"temp":     func(k *KeyInputs) { k.Temperature = 0.7 },
		"max_tok":  func(k *KeyInputs) { k.MaxTokens = 500 },
		"schema":   func(k *KeyInputs) { k.OutputSchema = `{"type":"array"}` },
	}
	for name, mutate := range mutations {
		in := baseInputs()
		mutate(&in)
		if Key(in) == Key(a) {
			t.Errorf("mutating %s must change the cache key", name)
		}
	}
}

func TestRedisRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := NewRedis(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()

	if _, ok := c.Get(ctx, "predict:none"); ok {
		t.Fatal("empty cache must miss")
	}

	c.Set(ctx, "predict:k1", []byte(`{"v":1}`), time.Minute)
	val, ok := c.Get(ctx, "predict:k1")
	if !ok || string(val) != `{"v":1}` {
		t.Fatalf("got (%q, %v), want stored value", val, ok)
	}

	// TTL expiry.
	mr.FastForward(2 * time.Minute)
	if _, ok := c.Get(ctx, "predict:k1"); ok {
		t.Fatal("entry must expire after TTL")
	}
}

func TestRedisUnreachableFailsAtBoot(t *testing.T) {
	if _, err := NewRedis("127.0.0.1:1", "", 0); err == nil {
		t.Fatal("connecting to a dead address must error")
	}
}

func TestRedisOutageIsAMiss(t *testing.T) {
	mr := miniredis.RunT(t)
	c, err := NewRedis(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()
	c.Set(ctx, "k", []byte("v"), time.Minute)

	mr.Close() // Redis goes down after boot

	if _, ok := c.Get(ctx, "k"); ok {
		t.Fatal("an unreachable Redis must read as a miss, not an error")
	}
	c.Set(ctx, "k2", []byte("v"), time.Minute) // must not panic
}

func TestMemoryRoundTripAndExpiry(t *testing.T) {
	now := time.Now()
	c := NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	c.Set(ctx, "k", []byte("v"), time.Minute)
	if val, ok := c.Get(ctx, "k"); !ok || string(val) != "v" {
		t.Fatalf("got (%q, %v), want stored value", val, ok)
	}

	now = now.Add(2 * time.Minute)
	if _, ok := c.Get(ctx, "k"); ok {
		t.Fatal("entry must expire after TTL")
	}
}
