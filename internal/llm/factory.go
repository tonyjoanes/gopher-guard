package llm

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	opsv1alpha1 "github.com/tonyjoanes/gopher-guard/api/v1alpha1"
	ggk8s "github.com/tonyjoanes/gopher-guard/internal/k8s"
)

// NewFromSpec builds an AnthropicClient for the given AegisWatch spec.
// The API key is read from the Kubernetes Secret named by spec.llmSecretRef (key: "apiKey").
func NewFromSpec(
	ctx context.Context,
	c client.Client,
	aw *opsv1alpha1.AegisWatch,
) (LLMClient, error) {
	spec := aw.Spec
	model := spec.LLMModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}

	apiKey, err := ggk8s.ReadSecretKey(ctx, c, aw.Namespace, spec.LLMSecretRef, "apiKey")
	if err != nil {
		return nil, fmt.Errorf("reading LLM API key secret %q: %w", spec.LLMSecretRef, err)
	}
	return NewAnthropicClient(model, apiKey), nil
}
