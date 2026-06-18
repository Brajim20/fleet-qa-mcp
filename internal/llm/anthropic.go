// Package llm is a minimal Anthropic Messages API client built for one job:
// running an agentic tool-use loop. It lets Claude drive the QA tools (read the
// issue, hit the API, grep the deployed source, …) the same way a human QA
// engineer would — deciding which tool to call next from what it has seen.
//
// Deliberately tiny: no streaming, no SDK. The qa package owns the tools and
// the safety gating; this package only threads the conversation.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	apiURL         = "https://api.anthropic.com/v1/messages"
	apiVersion     = "2023-06-01"
	defaultModel   = "claude-sonnet-4-6"
	defaultMaxTok  = 4096
	defaultTimeout = 120 * time.Second
)

// Client talks to the Anthropic Messages API.
type Client struct {
	APIKey    string
	Model     string
	MaxTokens int
	HTTP      *http.Client
}

// NewFromEnv builds a client from ANTHROPIC_API_KEY (+ optional ANTHROPIC_MODEL).
// The bool is false when no key is set — callers fall back to the deterministic
// engine in that case.
func NewFromEnv() (*Client, bool) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, false
	}
	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = defaultModel
	}
	return &Client{
		APIKey:    key,
		Model:     model,
		MaxTokens: defaultMaxTok,
		HTTP:      &http.Client{Timeout: defaultTimeout},
	}, true
}

// Block is one piece of message content. The same struct serves text,
// tool_use (assistant) and tool_result (user) blocks; omitempty keeps each
// wire form to just its relevant fields.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`        // text
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   string          `json:"content,omitempty"`     // tool_result (string form)
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

// Message is one conversational turn.
type Message struct {
	Role    string  `json:"role"`
	Content []Block `json:"content"`
}

// Tool is a function the model may call. InputSchema is a JSON Schema object.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type request struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	System      string    `json:"system,omitempty"`
	Temperature float64   `json:"temperature"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
}

type response struct {
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) create(ctx context.Context, system string, msgs []Message, tools []Tool) (*response, error) {
	body, _ := json.Marshal(request{
		Model: c.Model, MaxTokens: c.MaxTokens, System: system,
		Temperature: 0, Messages: msgs, Tools: tools,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != 200 {
		if r.Error != nil {
			return nil, fmt.Errorf("anthropic %s: %s", r.Error.Type, r.Error.Message)
		}
		return nil, fmt.Errorf("anthropic HTTP %d", resp.StatusCode)
	}
	return &r, nil
}

// DispatchFunc executes one tool call and returns its textual result. An error
// is reported back to the model as an is_error tool_result (so it can recover),
// not surfaced as a loop failure.
type DispatchFunc func(name string, input json.RawMessage) (string, error)

// RunToolLoop drives the agentic loop: call the model, run any tool_use blocks
// via dispatch, feed results back, repeat — until the model stops calling tools
// or maxTurns is hit. Returns the model's final assistant text and the full
// message history (for auditing / transcript storage).
func (c *Client) RunToolLoop(ctx context.Context, system string, msgs []Message, tools []Tool, dispatch DispatchFunc, maxTurns int) (string, []Message, error) {
	for turn := 0; turn < maxTurns; turn++ {
		resp, err := c.create(ctx, system, msgs, tools)
		if err != nil {
			return "", msgs, err
		}
		msgs = append(msgs, Message{Role: "assistant", Content: resp.Content})

		if resp.StopReason != "tool_use" {
			return collectText(resp.Content), msgs, nil
		}

		var results []Block
		for _, b := range resp.Content {
			if b.Type != "tool_use" {
				continue
			}
			out, derr := dispatch(b.Name, b.Input)
			res := Block{Type: "tool_result", ToolUseID: b.ID, Content: out}
			if derr != nil {
				res.IsError = true
				res.Content = "error: " + derr.Error()
			}
			results = append(results, res)
		}
		if len(results) == 0 {
			return collectText(resp.Content), msgs, nil
		}
		msgs = append(msgs, Message{Role: "user", Content: results})
	}
	return "", msgs, fmt.Errorf("tool loop exceeded %d turns without finishing", maxTurns)
}

func collectText(blocks []Block) string {
	var s string
	for _, b := range blocks {
		if b.Type == "text" {
			s += b.Text
		}
	}
	return s
}

func (c *Client) apiEndpoint() string {
	if u := os.Getenv("ANTHROPIC_BASE_URL"); u != "" { // test/proxy override
		return u
	}
	return apiURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
}
