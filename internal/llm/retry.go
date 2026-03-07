package llm

import (
	"context"
	"fmt"

	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

// diagnoseWithRetry calls diagnoseFn up to maxAttempts times, returning the
// first successful result or the last error.
func diagnoseWithRetry(
	ctx context.Context,
	obs *observability.ObservabilityContext,
	providerName string,
	maxAttempts int,
	diagnoseFn func(context.Context, *observability.ObservabilityContext) (*Diagnosis, error),
) (*Diagnosis, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		d, err := diagnoseFn(ctx, obs)
		if err == nil {
			return d, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%s diagnosis failed after %d attempts: %w", providerName, maxAttempts, lastErr)
}
