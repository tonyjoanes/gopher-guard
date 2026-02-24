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

const groqAPIURL = "https://api.groq.com/openai/v1/chat/completions"

// GroqClient calls the Groq inference API, which is OpenAI-compatible.
// Free tier supports llama3-70b-8192, mixtral-8x7b-32768, and others.
type GroqClient struct {
	Model  string
	APIKey string
	http   *http.Client
}

// NewGroqClient constructs a GroqClient with a 60-second timeout.
func NewGroqClient(model, apiKey string) *GroqClient {
	return &GroqClient{
		Model:  model,
		APIKey: apiKey,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Diagnose sends the observability context to Groq and parses the JSON response.
// It retries once on transient failures before returning an error.
func (g *GroqClient) Diagnose(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		d, err := g.diagnoseOnce(ctx, obs)
		if err == nil {
			return d, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("groq diagnosis failed after 2 attempts: %w", lastErr)
}

// --- OpenAI-compatible request/response types ---

type openAIRequest struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"` // "json_object"
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (g *GroqClient) diagnoseOnce(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	reqBody := openAIRequest{
		Model: g.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: BuildUserPrompt(obs)},
		},
		Temperature:    0.3,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.APIKey)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http POST groq: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, fmt.Errorf("decoding groq response: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("groq API error (%s): %s", apiResp.Error.Type, apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("groq returned no choices (status %d)", resp.StatusCode)
	}

	return parseDiagnosis(apiResp.Choices[0].Message.Content)
}

// parseDiagnosis decodes the LLM JSON content into a Diagnosis struct.
func parseDiagnosis(content string) (*Diagnosis, error) {
	var d Diagnosis
	if err := json.Unmarshal([]byte(content), &d); err != nil {
		return nil, fmt.Errorf("parsing LLM JSON response: %w\ncontent: %s", err, content)
	}
	if d.RootCause == "" {
		return nil, fmt.Errorf("LLM returned empty rootCause — likely a prompt/format issue")
	}
	return &d, nil
}
