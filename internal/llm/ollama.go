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

const defaultOllamaURL = "http://localhost:11434"

// OllamaClient calls a locally-running Ollama server.
// No API key is required — ideal for fully air-gapped / offline deployments.
type OllamaClient struct {
	Model   string
	BaseURL string
	http    *http.Client
}

// NewOllamaClient constructs an OllamaClient.
// baseURL defaults to "http://localhost:11434" when empty.
func NewOllamaClient(model, baseURL string) *OllamaClient {
	if baseURL == "" {
		baseURL = defaultOllamaURL
	}
	return &OllamaClient{
		Model:   model,
		BaseURL: baseURL,
		// Ollama can be slow on first token — allow 3 minutes.
		http: &http.Client{Timeout: 3 * time.Minute},
	}
}

// Diagnose sends the observability context to Ollama and parses the response.
// Retries once on transient failure.
func (o *OllamaClient) Diagnose(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		d, err := o.diagnoseOnce(ctx, obs)
		if err == nil {
			return d, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ollama diagnosis failed after 2 attempts: %w", lastErr)
}

// --- Ollama /api/chat request/response types ---

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"` // same shape as OpenAI
	Stream   bool            `json:"stream"`
	Format   string          `json:"format"` // "json" forces JSON output
	Options  ollamaOptions   `json:"options"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Error string `json:"error"`
}

func (o *OllamaClient) diagnoseOnce(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error) {
	reqBody := ollamaRequest{
		Model: o.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: BuildUserPrompt(obs)},
		},
		Stream: false,
		Format: "json",
		Options: ollamaOptions{
			Temperature: 0.3,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	endpoint := o.BaseURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http POST ollama: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var apiResp ollamaResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return nil, fmt.Errorf("decoding ollama response: %w", err)
	}
	if apiResp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", apiResp.Error)
	}

	return parseDiagnosis(apiResp.Message.Content)
}
