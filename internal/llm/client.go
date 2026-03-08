// Package llm provides the LLMClient abstraction and concrete implementations
// for diagnosing Kubernetes workload failures using an AI language model.
//
// The Diagnose call takes an ObservabilityContext (logs, events, metrics)
// and returns a Diagnosis with a root cause, an optional YAML patch, and
// a witty line for the operator logs.
package llm

import (
	"context"

	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

// LLMClient sends an observability snapshot to an LLM and returns a diagnosis.
type LLMClient interface {
	Diagnose(ctx context.Context, obs *observability.ObservabilityContext) (*Diagnosis, error)
}

// Diagnosis is the structured response returned by the LLM.
type Diagnosis struct {
	// RootCause is a concise technical explanation (1–2 sentences).
	RootCause string `json:"rootCause"`
	// YAMLPatch is a minimal strategic-merge-patch Deployment YAML snippet.
	// May be empty when the LLM has no confident fix to suggest.
	YAMLPatch string `json:"patch"`
	// WittyLine is a single gopher/Go-themed humorous observation about
	// the failure, printed alongside the root cause in the operator logs.
	WittyLine string `json:"wittyLine"`
}
