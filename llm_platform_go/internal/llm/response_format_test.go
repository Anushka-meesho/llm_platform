package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// When no ResponseFormat is set, the marshalled request must not contain a
// "response_format" key at all — guaranteeing every existing caller's wire form
// is byte-for-byte unchanged.
func TestChatRequest_OmitsResponseFormatWhenNil(t *testing.T) {
	b, err := json.Marshal(minimalReq())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "response_format") {
		t.Errorf("response_format leaked into wire form when unset: %s", b)
	}
}

func TestJSONObjectFormat_WireShape(t *testing.T) {
	req := minimalReq()
	req.ResponseFormat = JSONObjectFormat()

	var got map[string]any
	b, _ := json.Marshal(req)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rf, ok := got["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format missing or wrong type: %s", b)
	}
	if rf["type"] != "json_object" {
		t.Errorf("type: got %v, want json_object", rf["type"])
	}
	if _, hasSchema := rf["json_schema"]; hasSchema {
		t.Errorf("json_object form must not carry json_schema: %s", b)
	}
}

func TestJSONSchemaFormat_WireShape(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}`)
	req := minimalReq()
	req.ResponseFormat = JSONSchemaFormat("sentiment", schema, true)

	b, _ := json.Marshal(req)
	var got struct {
		ResponseFormat struct {
			Type   string `json:"type"`
			Schema struct {
				Name   string          `json:"name"`
				Schema json.RawMessage `json:"schema"`
				Strict bool            `json:"strict"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ResponseFormat.Type != "json_schema" {
		t.Errorf("type: got %q, want json_schema", got.ResponseFormat.Type)
	}
	if got.ResponseFormat.Schema.Name != "sentiment" {
		t.Errorf("name: got %q, want sentiment", got.ResponseFormat.Schema.Name)
	}
	if !got.ResponseFormat.Schema.Strict {
		t.Error("strict: got false, want true")
	}
	// The embedded schema must round-trip unchanged.
	var inner map[string]any
	if err := json.Unmarshal(got.ResponseFormat.Schema.Schema, &inner); err != nil {
		t.Fatalf("embedded schema not valid JSON: %v", err)
	}
	if inner["type"] != "object" {
		t.Errorf("embedded schema type: got %v, want object", inner["type"])
	}
}

func TestParseResponseFormat(t *testing.T) {
	if rf := parseResponseFormat(nil); rf != nil {
		t.Errorf("nil raw should parse to nil, got %+v", rf)
	}
	if rf := parseResponseFormat(json.RawMessage(`{"oops":1}`)); rf != nil {
		t.Errorf("directive without a type should parse to nil, got %+v", rf)
	}
	if rf := parseResponseFormat(json.RawMessage(`not json`)); rf != nil {
		t.Errorf("malformed directive should parse to nil, got %+v", rf)
	}
	rf := parseResponseFormat(json.RawMessage(`{"type":"json_object"}`))
	if rf == nil || rf.Type != "json_object" {
		t.Errorf("json_object directive parsed wrong: %+v", rf)
	}
}

// "Prefer if possible, else basic": when the provider rejects the request with a
// 400 because of the response_format directive, CallModel must drop it and retry
// the same model in plain mode — and that retry's success is the answer.
func TestCallModel_DegradesOnStructuredRejection(t *testing.T) {
	var sawFormat []bool // response_format present on each upstream call, in order
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		sawFormat = append(sawFormat, req.ResponseFormat != nil)
		if req.ResponseFormat != nil {
			w.WriteHeader(http.StatusBadRequest) // provider can't honour the directive
			w.Write([]byte(`{"error":{"message":"response_format not supported"}}`))
			return
		}
		json.NewEncoder(w).Encode(okResponse("plain answer", 5, 7))
	}))
	defer srv.Close()

	// Register a throwaway model pointed at the test server.
	const model = "test-degrade-model"
	registry[model] = providerConfig{modelID: "m", provider: "test",
		clientFn: func(*Clients) Provider { return newProvider(srv, "k") }}
	defer delete(registry, model)

	res := CallModel(context.Background(), &Clients{}, model,
		[]ChatMessage{{Role: "user", Content: "hi"}}, 0.7, 100,
		JSONObjectFormat())

	if !res.Success {
		t.Fatalf("expected eventual success after degradation, got error: %v", res.Error)
	}
	if res.Response == nil || *res.Response != "plain answer" {
		t.Errorf("expected the plain-mode answer, got %v", res.Response)
	}
	if len(sawFormat) != 2 || !sawFormat[0] || sawFormat[1] {
		t.Errorf("expected call#1 WITH format then call#2 WITHOUT; got %v", sawFormat)
	}
}

// When the provider accepts the directive, there must be no second call.
func TestCallModel_NoDegradeOnSuccess(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(okResponse(`{"ok":true}`, 5, 7))
	}))
	defer srv.Close()

	const model = "test-ok-model"
	registry[model] = providerConfig{modelID: "m", provider: "test",
		clientFn: func(*Clients) Provider { return newProvider(srv, "k") }}
	defer delete(registry, model)

	res := CallModel(context.Background(), &Clients{}, model,
		[]ChatMessage{{Role: "user", Content: "hi"}}, 0.7, 100, JSONObjectFormat())
	if !res.Success {
		t.Fatalf("expected success, got %v", res.Error)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 upstream call, got %d", calls)
	}
}

// The provider must forward response_format to the upstream endpoint verbatim.
func TestProvider_ForwardsResponseFormat(t *testing.T) {
	var decoded chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&decoded) //nolint:errcheck
		json.NewEncoder(w).Encode(okResponse("ok", 1, 1))
	}))
	defer srv.Close()

	req := minimalReq()
	req.ResponseFormat = JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true)
	newProvider(srv, "key").Call(context.Background(), req) //nolint:errcheck

	if decoded.ResponseFormat == nil {
		t.Fatal("response_format not forwarded to provider")
	}
	if decoded.ResponseFormat.Type != "json_schema" || decoded.ResponseFormat.Schema == nil {
		t.Errorf("forwarded response_format wrong: %+v", decoded.ResponseFormat)
	}
}
