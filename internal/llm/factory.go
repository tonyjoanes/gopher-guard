package llm

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	opsv1alpha1 "github.com/tonyjoanes/gopher-guard/api/v1alpha1"
)

// NewFromSpec builds the appropriate LLMClient for the given AegisWatch spec.
//
// For Groq and OpenAI: reads the API key from the Kubernetes Secret named
// by spec.llmSecretRef (key: "apiKey") in the same namespace as the CR.
//
// For Ollama: no secret is required; the BaseURL defaults to localhost:11434
// and can be overridden by setting spec.llmSecretRef to a secret that contains
// a "baseUrl" key.
func NewFromSpec(
	ctx context.Context,
	c client.Client,
	aw *opsv1alpha1.AegisWatch,
) (LLMClient, error) {
	spec := aw.Spec
	model := spec.LLMModel
	if model == "" {
		model = defaultModelFor(spec.LLMProvider)
	}

	switch spec.LLMProvider {
	case opsv1alpha1.LLMProviderGroq, opsv1alpha1.LLMProviderOpenAI:
		apiKey, err := readSecretKey(ctx, c, aw.Namespace, spec.LLMSecretRef, "apiKey")
		if err != nil {
			return nil, fmt.Errorf("reading LLM API key secret %q: %w", spec.LLMSecretRef, err)
		}
		// Both Groq and OpenAI use the same OpenAI-compatible client;
		// Groq just uses a different base URL (handled inside GroqClient).
		return NewGroqClient(model, apiKey), nil

	case opsv1alpha1.LLMProviderOllama:
		baseURL := ""
		// Optional: allow overriding the Ollama URL via a secret key "baseUrl".
		if spec.LLMSecretRef != "" {
			u, err := readSecretKey(ctx, c, aw.Namespace, spec.LLMSecretRef, "baseUrl")
			if err == nil {
				baseURL = u
			}
			// If the key doesn't exist that's fine — we fall back to localhost.
		}
		return NewOllamaClient(model, baseURL), nil

	default:
		return nil, fmt.Errorf("unsupported llmProvider %q — must be one of: groq, ollama, openai", spec.LLMProvider)
	}
}

// defaultModelFor returns a sensible default model name for each provider.
func defaultModelFor(provider opsv1alpha1.LLMProvider) string {
	switch provider {
	case opsv1alpha1.LLMProviderGroq:
		return "llama3-70b-8192"
	case opsv1alpha1.LLMProviderOpenAI:
		return "gpt-4o-mini"
	case opsv1alpha1.LLMProviderOllama:
		return "llama3"
	default:
		return "llama3-70b-8192"
	}
}

// readSecretKey fetches a single string value from a Kubernetes Secret.
func readSecretKey(ctx context.Context, c client.Client, namespace, secretName, key string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty")
	}

	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      secretName,
	}, &secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}

	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, secretName, key)
	}
	return string(val), nil
}
