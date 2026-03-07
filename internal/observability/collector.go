package observability

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Collector assembles an ObservabilityContext for a troubled Deployment.
// It is safe to call concurrently.
type Collector struct {
	// CtrlClient is the controller-runtime cached client (used for Events + Pods list).
	CtrlClient client.Client
	// KubeClient is the raw clientset required for pod log streaming.
	KubeClient kubernetes.Interface
	// Prometheus is optional; set URL to "" to skip metrics collection.
	Prometheus *PrometheusClient
}

// Collect builds a complete ObservabilityContext for the given Deployment.
// Non-fatal errors (log fetch failure, Prometheus unavailable) are logged and
// skipped so the LLM still receives partial context rather than nothing.
func (c *Collector) Collect(
	ctx context.Context,
	deployment *appsv1.Deployment,
	anomalyReason string,
) (*ObservabilityContext, error) {
	logger := log.FromContext(ctx).WithValues(
		"deployment", deployment.Namespace+"/"+deployment.Name,
	)

	obs := &ObservabilityContext{
		DeploymentName: deployment.Name,
		Namespace:      deployment.Namespace,
		AnomalyReason:  anomalyReason,
		CollectedAt:    time.Now(),
	}

	// --- 1. List pods for this Deployment ---
	if deployment.Spec.Selector == nil {
		return nil, fmt.Errorf("deployment %s/%s has no selector", deployment.Namespace, deployment.Name)
	}
	var podList corev1.PodList
	if err := c.CtrlClient.List(ctx, &podList,
		client.InNamespace(deployment.Namespace),
		client.MatchingLabels(deployment.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	// Build a set of pod+deployment names for event filtering.
	involvedNames := map[string]bool{deployment.Name: true}
	for _, pod := range podList.Items {
		involvedNames[pod.Name] = true
	}

	// --- 2. Collect per-pod snapshots (logs fetched concurrently per container) ---
	obs.Pods = c.collectPodSnapshots(ctx, podList.Items)

	// --- 3 & 4. Fetch events and Prometheus metrics concurrently ---
	var (
		wg      sync.WaitGroup
		evErr   error
		promErr error
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		events, err := FetchKubeEvents(ctx, c.CtrlClient, deployment.Namespace, involvedNames)
		if err != nil {
			evErr = err
			return
		}
		obs.KubeEvents = events
	}()

	if c.Prometheus != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			metrics, err := c.Prometheus.QueryWorkload(ctx, deployment.Namespace, deployment.Name)
			if err != nil {
				promErr = err
				return
			}
			obs.Metrics = metrics
		}()
	}

	wg.Wait()

	if evErr != nil {
		logger.V(1).Info("could not fetch kube events", "err", evErr)
	}
	if promErr != nil {
		logger.V(1).Info("prometheus query failed — continuing without metrics", "err", promErr)
	}

	logger.Info("📦 ObservabilityContext collected",
		"pods", len(obs.Pods),
		"events", len(obs.KubeEvents),
		"hasMetrics", obs.Metrics != nil,
	)

	return obs, nil
}

// collectPodSnapshots builds PodSnapshot entries, fetching container logs
// concurrently across all containers in all pods.
func (c *Collector) collectPodSnapshots(ctx context.Context, pods []corev1.Pod) []PodSnapshot {
	type indexedSnap struct {
		idx  int
		snap PodSnapshot
	}

	results := make(chan indexedSnap, len(pods))
	var wg sync.WaitGroup

	for i := range pods {
		pod := &pods[i]
		wg.Add(1)
		go func(idx int, pod *corev1.Pod) {
			defer wg.Done()
			snap := PodSnapshot{
				Name:     pod.Name,
				Phase:    string(pod.Status.Phase),
				NodeName: pod.Spec.NodeName,
			}

			// Fetch logs for each container concurrently within the pod.
			type containerResult struct {
				cs   corev1.ContainerStatus
				logs string
			}
			cResults := make(chan containerResult, len(pod.Status.ContainerStatuses))
			var cwg sync.WaitGroup
			for _, cs := range pod.Status.ContainerStatuses {
				cs := cs
				cwg.Add(1)
				go func() {
					defer cwg.Done()
					logs, err := FetchContainerLogs(ctx, c.KubeClient, pod.Namespace, pod.Name, cs.Name)
					if err != nil {
						logs = fmt.Sprintf("[log unavailable: %v]", err)
					}
					cResults <- containerResult{cs: cs, logs: logs}
				}()
			}
			cwg.Wait()
			close(cResults)

			for cr := range cResults {
				snap.Containers = append(snap.Containers, ContainerSnapshot{
					Name:         cr.cs.Name,
					Image:        imageForContainer(pod, cr.cs.Name),
					RestartCount: cr.cs.RestartCount,
					State:        containerStateString(cr.cs),
					LastLogs:     truncateLogs(cr.logs),
				})
			}

			results <- indexedSnap{idx: idx, snap: snap}
		}(i, pod)
	}

	wg.Wait()
	close(results)

	// Reassemble in original pod order.
	snapshots := make([]PodSnapshot, len(pods))
	for r := range results {
		snapshots[r.idx] = r.snap
	}
	return snapshots
}

// containerStateString produces a compact state string for a ContainerStatus.
func containerStateString(cs corev1.ContainerStatus) string {
	if cs.State.Running != nil {
		return "running"
	}
	if w := cs.State.Waiting; w != nil {
		return "waiting:" + w.Reason
	}
	if t := cs.State.Terminated; t != nil {
		return fmt.Sprintf("terminated:%s:exit%d", t.Reason, t.ExitCode)
	}
	return "unknown"
}

// imageForContainer looks up the image name from the pod spec containers.
func imageForContainer(pod *corev1.Pod, containerName string) string {
	for _, c := range pod.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return "unknown"
}

// truncateLogs caps log content at collection time to avoid holding large
// strings in memory and to keep LLM prompt sizes predictable.
const maxLogChars = 3000

func truncateLogs(logs string) string {
	if len(logs) <= maxLogChars {
		return logs
	}
	return "...[truncated]...\n" + logs[len(logs)-maxLogChars:]
}
