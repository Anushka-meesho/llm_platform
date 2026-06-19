package llm

import (
	"context"
	"net/http"
	"testing"
)

// stubGate is a HealthGate that marks the named models unhealthy (skipped
// without a call) and records nothing — enough to exercise the skipped-unhealthy
// attempt path.
type stubGate struct{ unhealthy map[string]bool }

func (g stubGate) Allow(model string) bool       { return !g.unhealthy[model] }
func (g stubGate) RecordSuccess(model string)    {}
func (g stubGate) RecordFailure(model, r string) {}

func TestFallbackAttemptsTrace(t *testing.T) {
	_ = LoadPricingFromMap(map[string]Rate{})
	msgs := []ChatMessage{{Role: "user", Content: "hi"}}

	t.Run("infra failure then fallback success records both attempts", func(t *testing.T) {
		ok := okServer(t, "served-by-fallback")
		clients := &Clients{
			Meesho: deadProvider(),                       // primary gpt-4o-mini → dead
			Groq:   NewOpenAICompatProvider(ok.URL, "k"), // fallback llama-groq → ok
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)

		if len(r.Attempts) != 2 {
			t.Fatalf("expected 2 attempts, got %d: %+v", len(r.Attempts), r.Attempts)
		}
		a0 := r.Attempts[0]
		if a0.Seq != 0 || a0.Model != "gpt-4o-mini" || a0.Outcome != "error" {
			t.Errorf("attempt 0 wrong: %+v", a0)
		}
		if a0.FallbackReason == "" {
			t.Errorf("attempt 0 should carry the fallback reason (why the walk advanced)")
		}
		if !a0.InfraFailure {
			t.Errorf("attempt 0 (dead provider) should be flagged infra_failure")
		}
		a1 := r.Attempts[1]
		if a1.Seq != 1 || a1.Model != "llama-groq" || a1.Outcome != "success" {
			t.Errorf("attempt 1 wrong: %+v", a1)
		}
		if !a1.FallbackUsed || a1.FallbackReason != "" {
			t.Errorf("the serving fallback attempt should have no fallback reason: %+v", a1)
		}
	})

	t.Run("definitive 400 records a single error attempt with no fallback reason", func(t *testing.T) {
		bad := badRequestServer(t)
		ok := okServer(t, "should-not-be-reached")
		clients := &Clients{
			Meesho: NewOpenAICompatProvider(bad.URL, "k"),
			Groq:   NewOpenAICompatProvider(ok.URL, "k"),
		}
		r := CallWithFallback(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100)

		if len(r.Attempts) != 1 {
			t.Fatalf("400 must stop the walk at one attempt, got %d", len(r.Attempts))
		}
		a := r.Attempts[0]
		if a.Outcome != "error" || a.HTTPStatus != http.StatusBadRequest {
			t.Errorf("expected a 400 error attempt, got %+v", a)
		}
		if a.FallbackReason != "" {
			t.Errorf("a definitive 400 should not advance the chain, so no fallback reason: %+v", a)
		}
	})

	t.Run("schema_invalid attempt keeps the returned content (and its cost)", func(t *testing.T) {
		// Primary answers but fails validation → fallback serves a valid answer.
		// The schema-invalid attempt must preserve what the model returned (it
		// still cost tokens) so the trace shows the wasted output.
		primary := okServer(t, "not-valid-json")
		fb := okServer(t, "valid")
		clients := &Clients{
			Meesho: NewOpenAICompatProvider(primary.URL, "k"),
			Groq:   NewOpenAICompatProvider(fb.URL, "k"),
		}
		// Reject only the primary's content so it falls back.
		opts := FallbackOptions{Validate: func(text string) bool { return text != "not-valid-json" }}
		r := CallWithFallbackOpts(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100, opts)

		if len(r.Attempts) != 2 {
			t.Fatalf("expected schema-invalid + served attempts, got %d: %+v", len(r.Attempts), r.Attempts)
		}
		bad := r.Attempts[0]
		if bad.Outcome != "schema_invalid" {
			t.Fatalf("attempt 0 should be schema_invalid, got %q", bad.Outcome)
		}
		if bad.Response == nil || *bad.Response != "not-valid-json" {
			t.Errorf("schema_invalid attempt should preserve the returned content, got %v", bad.Response)
		}
		// The call succeeded upstream, so the wasted attempt has token usage.
		if bad.TotalTokens == 0 {
			t.Errorf("schema_invalid attempt should carry the tokens it cost, got 0")
		}
	})

	t.Run("whole chain fails validation → degraded (flagged, not failed) with every model's output in the trace", func(t *testing.T) {
		// All three models answer at the API level but every answer fails the
		// output schema. This is the "flagged, not failed" contract: the result
		// stays a (degraded) success carrying the raw response — it must NOT get a
		// misleading "all models failed" error pasted on. And the trace must keep
		// all three models' outputs so an operator can see what each returned.
		meesho := okServer(t, "m-out")
		groq := okServer(t, "g-out")
		clients := &Clients{
			Meesho: NewOpenAICompatProvider(meesho.URL, "k"),
			Groq:   NewOpenAICompatProvider(groq.URL, "k"),
		}
		opts := FallbackOptions{Validate: func(string) bool { return false }} // reject everything
		r := CallWithFallbackOpts(context.Background(), clients,
			[]string{"gpt-4o-mini", "gemini-2.5-flash", "llama-groq"}, msgs, 0.1, 100, opts)

		if !r.Success {
			t.Errorf("all-invalid output is flagged, not failed — Success must stay true")
		}
		if !r.Degraded {
			t.Errorf("a chain that exhausted every model is degraded, got %+v", r)
		}
		if r.Error != nil {
			t.Errorf("must not paste an 'all models failed' error onto a flagged-invalid success, got %q", *r.Error)
		}
		if len(r.Attempts) != 3 {
			t.Fatalf("expected one attempt per model (3), got %d", len(r.Attempts))
		}
		for i, a := range r.Attempts {
			if a.Outcome != "schema_invalid" {
				t.Errorf("attempt %d should be schema_invalid, got %q", i, a.Outcome)
			}
			if a.Response == nil || *a.Response == "" {
				t.Errorf("attempt %d should preserve the model's returned output", i)
			}
		}
	})

	t.Run("skipped-unhealthy model is recorded as its own attempt", func(t *testing.T) {
		ok := okServer(t, "served-by-fallback")
		clients := &Clients{
			Meesho: deadProvider(),
			Groq:   NewOpenAICompatProvider(ok.URL, "k"),
		}
		opts := FallbackOptions{Gate: stubGate{unhealthy: map[string]bool{"gpt-4o-mini": true}}}
		r := CallWithFallbackOpts(context.Background(), clients,
			[]string{"gpt-4o-mini", "llama-groq"}, msgs, 0.1, 100, opts)

		if len(r.Attempts) != 2 {
			t.Fatalf("expected skipped + served attempts, got %d: %+v", len(r.Attempts), r.Attempts)
		}
		if r.Attempts[0].Outcome != "skipped_unhealthy" || r.Attempts[0].FallbackReason == "" {
			t.Errorf("attempt 0 should be a skipped-unhealthy with a reason: %+v", r.Attempts[0])
		}
		if r.Attempts[1].Outcome != "success" {
			t.Errorf("attempt 1 should serve the answer: %+v", r.Attempts[1])
		}
	})
}
