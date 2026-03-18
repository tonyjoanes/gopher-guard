package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	anthropicMaxTokens = 1024
)

// AnthropicClient calls the Anthropic Messages API (Claude models).
type AnthropicClient struct {
	Model  string
	APIKey string
	http   *http.Client
}

// NewAnthropicClient constructs an AnthropicClient with a 60-second timeout.
func NewAnthropicClient(model, apiKey string) *AnthropicClient {
	return &AnthropicClient{
		Model:  model,
		APIKey: apiKey,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Diagnose sends the observability context to Anthropic and parses the JSON response.
// It retries once on transient failures before returning an error.
func (a *AnthropicClient) Diagnose(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	return diagnoseWithRetry(ctx, obs, "anthropic", 2, a.diagnoseOnce)
}

// --- Anthropic Messages API request/response types ---

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *AnthropicClient) diagnoseOnce(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	reqBody := anthropicRequest{
		Model:     a.Model,
		MaxTokens: anthropicMaxTokens,
		System:    SystemPrompt,
		Messages: []anthropicMessage{
			{Role: "user", Content: BuildUserPrompt(obs)},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http POST anthropic: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, fmt.Errorf("decoding anthropic response: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("anthropic API error (%s): %s", apiResp.Error.Type, apiResp.Error.Message)
	}
	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("anthropic returned no content (status %d)", resp.StatusCode)
	}

	// Find the first text block in the response.
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			return parseDiagnosis(block.Text)
		}
	}
	return nil, fmt.Errorf("anthropic response contained no text block (status %d)", resp.StatusCode)
}
