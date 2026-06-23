package types

import "encoding/json"

// Message is one turn in a conversation.
// Content is interface{} to support both plain strings and multimodal arrays.
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type RunRequest struct {
	Prompt             string               `json:"prompt"`
	Models             []string             `json:"models,omitempty"`
	ModelConversations map[string][]Message `json:"model_conversations,omitempty"`
	Temperature        *float64             `json:"temperature,omitempty"` // nil → default 0.7
	MaxTokens          *int                 `json:"max_tokens,omitempty"`  // nil → default 1000
	SessionID          string               `json:"session_id,omitempty"`
	SystemPrompt       string               `json:"system_prompt,omitempty"`
	// ResponseFormat is an optional OpenAI-style structured-output directive
	// ({"type":"json_object"} or {"type":"json_schema",...}). When set, each model
	// is asked to honour it; a model that cannot falls back to a plain call (see
	// llm.CallModel). Carried as raw JSON so this package stays free of any llm
	// dependency; the llm layer decodes it. Absent → unchanged behaviour.
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

type DeleteSessionsRequest struct {
	SessionIDs []string `json:"session_ids"`
}
