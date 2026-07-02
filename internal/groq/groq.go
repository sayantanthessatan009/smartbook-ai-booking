// Package groq is a tiny, dependency-free client for the Groq Cloud
// chat-completions endpoint (OpenAI-compatible). It is the only "AI" call
// used anywhere in this app, and it is designed to fail soft: if no API key
// is configured, or Groq is unreachable/rate-limited, callers get a clear
// error and the app degrades to its non-AI behaviour rather than crashing.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const defaultBaseURL = "https://api.groq.com/openai/v1/chat/completions"

// Fast is tuned for high free-tier request budget (14,400 req/day) — used for
// latency-sensitive, simple tasks like drafting confirmation messages.
const Fast = "llama-3.1-8b-instant"

// Smart is tuned for quality on harder reasoning like intent extraction and
// conflict mediation. Free tier budget is smaller (~1,000 req/day) so it's
// used only where reasoning quality clearly matters.
const Smart = "llama-3.3-70b-versatile"

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewFromEnv builds a client from the GROQ_API_KEY environment variable.
// The returned client is never nil; call IsConfigured() to check readiness.
func NewFromEnv() *Client {
	base := os.Getenv("GROQ_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	return &Client{
		apiKey:  os.Getenv("GROQ_API_KEY"),
		baseURL: base,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) IsConfigured() bool { return c.apiKey != "" }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type respFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends a system+user prompt pair and returns the model's text.
// If asJSON is true, it requests a JSON-only response (used for structured
// intent extraction) via response_format.
func (c *Client) Complete(ctx context.Context, model, systemPrompt, userPrompt string, asJSON bool) (string, error) {
	if !c.IsConfigured() {
		return "", errors.New("groq: GROQ_API_KEY is not set")
	}

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.4,
		MaxTokens:   700,
	}
	if asJSON {
		reqBody.ResponseFormat = &respFormat{Type: "json_object"}
	}

	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("groq: bad response (status %d): %s", resp.StatusCode, truncate(string(body), 300))
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", errors.New("groq: rate limit hit (free tier) — try again shortly")
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("groq: %s", parsed.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("groq: http %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("groq: empty response")
	}
	return parsed.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
