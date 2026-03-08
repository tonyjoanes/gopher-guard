package observability

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maxEvents = 20

// FetchKubeEvents returns the most recent Warning events in the given namespace,
// optionally filtered to events whose InvolvedObject name matches one of the
// provided names (e.g. deployment name + pod names).
//
// Events are sorted newest-first and capped at maxEvents.
func FetchKubeEvents(
	ctx context.Context,
	c client.Client,
	namespace string,
	involvedNames map[string]bool,
) ([]KubeEvent, error) {
	var eventList corev1.EventList
	if err := c.List(ctx, &eventList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing events in %s: %w", namespace, err)
	}

	var out []KubeEvent
	for _, ev := range eventList.Items {
		// Filter to Warning events, or any event involving our objects.
		if ev.Type != corev1.EventTypeWarning && !involvedNames[ev.InvolvedObject.Name] {
			continue
		}
		last := ev.LastTimestamp.Time
		if last.IsZero() && ev.EventTime.Time != (ev.EventTime.Time).Truncate(0) {
			last = ev.EventTime.Time
		}
		out = append(out, KubeEvent{
			Type:           ev.Type,
			Reason:         ev.Reason,
			Message:        ev.Message,
			Count:          ev.Count,
			LastSeen:       last,
			InvolvedObject: ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name,
		})
	}

	// Sort newest-first.
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeen.After(out[j].LastSeen)
	})

	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out, nil
}
