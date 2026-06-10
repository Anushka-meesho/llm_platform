package llm

import (
	"llm_platform_go/internal/config"

	openai "github.com/sashabaranov/go-openai"
)

// Clients holds one configured HTTP client per provider.
// All three use the OpenAI wire format — only the base URL and key differ.
type Clients struct {
	OpenAI *openai.Client
	Groq   *openai.Client
	Gemini *openai.Client
}

func BuildClients(cfg *config.Config) *Clients {
	groqCfg := openai.DefaultConfig(cfg.GroqKey)
	groqCfg.BaseURL = "https://api.groq.com/openai/v1"

	geminiCfg := openai.DefaultConfig(cfg.GeminiKey)
	geminiCfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai/"

	return &Clients{
		OpenAI: openai.NewClient(cfg.OpenAIKey),
		Groq:   openai.NewClientWithConfig(groqCfg),
		Gemini: openai.NewClientWithConfig(geminiCfg),
	}
}
