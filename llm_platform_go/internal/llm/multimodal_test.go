package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Text-only messages must keep the exact legacy wire form: content is a plain
// string. This guards the back-compat promise of ChatMessage.MarshalJSON.
func TestChatMessage_TextOnlyMarshalsAsString(t *testing.T) {
	b, err := json.Marshal(ChatMessage{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"role":"user","content":"hello"}` {
		t.Errorf("text-only message changed wire form: %s", got)
	}
}

// A message with images must marshal to the OpenAI-compatible multimodal array:
// a text part followed by image_url parts carrying the data URL.
func TestChatMessage_WithImageMarshalsAsMultimodalArray(t *testing.T) {
	const dataURL = "data:image/jpeg;base64,AAAA"
	b, err := json.Marshal(ChatMessage{
		Role:    "user",
		Content: "describe this",
		Images:  []string{dataURL},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var msg struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatalf("content is not the expected multimodal array: %v (%s)", err, b)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("want 2 content parts (text + image), got %d: %s", len(msg.Content), b)
	}
	if msg.Content[0].Type != "text" || msg.Content[0].Text != "describe this" {
		t.Errorf("first part should be the text: %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "image_url" || msg.Content[1].ImageURL.URL != dataURL {
		t.Errorf("second part should carry the image data URL: %+v", msg.Content[1])
	}
	if !strings.Contains(string(b), dataURL) {
		t.Errorf("data URL missing from wire body: %s", b)
	}
}
