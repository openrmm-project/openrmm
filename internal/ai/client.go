// Package ai implements the chat assistant: an OpenAI-compatible client plus a
// tool-calling loop that lets the model read fleet state and propose actions
// (which always require human approval before they run).
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message mirrors the OpenAI-compatible chat message shape.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the name and JSON-encoded arguments of a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a tool advertised to the model.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a callable function (JSON Schema parameters).
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	Temperature float64   `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Client talks to an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	mu      sync.RWMutex
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	breaker breaker
}

// Update swaps provider settings without rebuilding the server.
//
// It also closes the circuit: an operator changing these fields is usually
// fixing exactly what tripped it, and making them wait out a cooldown to find
// out whether the fix worked would be its own small outage.
func (c *Client) Update(baseURL, apiKey, model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(baseURL, "/")
	c.apiKey = apiKey
	c.model = model
	c.breaker.success()
}

// NewClient builds an AI client. baseURL should include the API version path,
// e.g. https://api.openai.com/v1 or http://localhost:11434/v1 (Ollama).
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Chat performs one completion request.
//
// It is guarded by a circuit breaker. Only faults that indicate an unhealthy
// provider count against it — transport errors (which include the 120s timeout),
// 5xx, and 429. A 401 or 400 is deterministic and already fails instantly with a
// message the operator can act on, so tripping on those would replace a useful
// error with a vague one and gain nothing: there is no pile-up to prevent.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*Message, error) {
	if err := c.breaker.allow(time.Now()); err != nil {
		return nil, err
	}
	c.mu.RLock()
	baseURL, apiKey, model := c.baseURL, c.apiKey, c.model
	c.mu.RUnlock()
	reqBody, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    messages,
		Tools:       tools,
		Temperature: 0.2,
	})
	if err != nil {
		c.breaker.abort() // our own encoding bug, not the provider's fault
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		c.breaker.abort()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Covers the timeout, connection refused and DNS failures — the slow and
		// unreachable cases the breaker exists for.
		c.breaker.failure(time.Now())
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		if unhealthyStatus(resp.StatusCode) {
			c.breaker.failure(time.Now())
		} else {
			c.breaker.abort()
		}
		return nil, fmt.Errorf("ai provider returned %d: %s", resp.StatusCode, string(body))
	}
	// The provider answered in time, so it is healthy as far as this breaker is
	// concerned, even if the payload turns out to be unusable below.
	c.breaker.success()
	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode ai response: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("ai provider error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("ai provider returned no choices")
	}
	return &out.Choices[0].Message, nil
}

// unhealthyStatus reports whether a status means the provider is in trouble
// rather than the request being wrong. 429 counts: being rate limited is a
// capacity problem, and hammering through it makes it worse.
func unhealthyStatus(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests
}
