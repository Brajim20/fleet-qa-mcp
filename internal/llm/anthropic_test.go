package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRunToolLoop drives the loop against a mock Anthropic server that asks for
// one tool call, then ends — verifying dispatch runs, the result is threaded
// back, and the final text is returned.
func TestRunToolLoop(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			t.Error("missing x-api-key header")
		}
		body, _ := io.ReadAll(r.Body)
		calls++
		switch calls {
		case 1:
			// First turn: request a tool call.
			io.WriteString(w, `{"stop_reason":"tool_use","content":[
				{"type":"text","text":"Let me check the API."},
				{"type":"tool_use","id":"toolu_1","name":"fleet_request","input":{"path":"/api/x"}}
			]}`)
		default:
			// Second turn: the tool result must have been threaded in.
			if !strings.Contains(string(body), "HTTP 200 ok") {
				t.Errorf("tool result not threaded back to model; body=%s", body)
			}
			io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"done: looks fixed"}]}`)
		}
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	c := &Client{APIKey: "test", Model: "m", MaxTokens: 100, HTTP: srv.Client()}

	var dispatched string
	dispatch := func(name string, input json.RawMessage) (string, error) {
		dispatched = name
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(input, &in)
		if in.Path != "/api/x" {
			t.Errorf("tool input not decoded: got path %q", in.Path)
		}
		return "HTTP 200 ok", nil
	}

	final, history, err := c.RunToolLoop(context.Background(), "sys",
		[]Message{{Role: "user", Content: []Block{{Type: "text", Text: "investigate"}}}},
		[]Tool{{Name: "fleet_request", Description: "d", InputSchema: map[string]any{"type": "object"}}},
		dispatch, 5)
	if err != nil {
		t.Fatalf("RunToolLoop error: %v", err)
	}
	if dispatched != "fleet_request" {
		t.Errorf("dispatch not called with the tool; got %q", dispatched)
	}
	if final != "done: looks fixed" {
		t.Errorf("final text = %q, want %q", final, "done: looks fixed")
	}
	// user(investigate) + assistant(tool_use) + user(tool_result) + assistant(end_turn)
	if len(history) != 4 {
		t.Errorf("history length = %d, want 4", len(history))
	}
	if calls != 2 {
		t.Errorf("model calls = %d, want 2", calls)
	}
}

func TestRunToolLoopMaxTurns(t *testing.T) {
	// Server always asks for another tool call → loop must stop at maxTurns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]}`)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	c := &Client{APIKey: "k", Model: "m", MaxTokens: 50, HTTP: srv.Client()}
	_, _, err := c.RunToolLoop(context.Background(), "", []Message{{Role: "user", Content: []Block{{Type: "text", Text: "go"}}}},
		nil, func(string, json.RawMessage) (string, error) { return "ok", nil }, 3)
	if err == nil || !strings.Contains(err.Error(), "exceeded 3 turns") {
		t.Errorf("expected max-turns error, got %v", err)
	}
}

func TestNewFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, ok := NewFromEnv(); ok {
		t.Error("NewFromEnv should be false with no key")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-xxx")
	t.Setenv("ANTHROPIC_MODEL", "")
	c, ok := NewFromEnv()
	if !ok || c.Model != defaultModel {
		t.Errorf("NewFromEnv with key: ok=%v model=%q (want default %q)", ok, c.Model, defaultModel)
	}
	t.Setenv("ANTHROPIC_MODEL", "claude-custom")
	c, _ = NewFromEnv()
	if c.Model != "claude-custom" {
		t.Errorf("model override not honored: %q", c.Model)
	}
}
