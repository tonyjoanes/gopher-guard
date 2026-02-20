package observability

import (
	"context"
	"fmt"
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

	// --- 2. Collect per-pod snapshots (logs included) ---
	for i := range podList.Items {
		pod := &podList.Items[i]
		snap := PodSnapshot{
			Name:     pod.Name,
			Phase:    string(pod.Status.Phase),
			NodeName: pod.Spec.NodeName,
		}

		for _, cs := range pod.Status.ContainerStatuses {
			state := containerStateString(cs)
			logs, err := FetchContainerLogs(ctx, c.KubeClient, pod.Namespace, pod.Name, cs.Name)
			if err != nil {
				logger.V(1).Info("could not fetch container logs",
					"pod", pod.Name, "container", cs.Name, "err", err)
				logs = fmt.Sprintf("[log unavailable: %v]", err)
			}

			// Find the image from the spec (status only has imageID).
			image := imageForContainer(pod, cs.Name)

			snap.Containers = append(snap.Containers, ContainerSnapshot{
				Name:         cs.Name,
				Image:        image,
				RestartCount: cs.RestartCount,
				State:        state,
				LastLogs:     logs,
			})
		}
		obs.Pods = append(obs.Pods, snap)
	}

	// --- 3. Fetch Kubernetes Warning events ---
	events, err := FetchKubeEvents(ctx, c.CtrlClient, deployment.Namespace, involvedNames)
	if err != nil {
		logger.V(1).Info("could not fetch kube events", "err", err)
	} else {
		obs.KubeEvents = events
	}

	// --- 4. Fetch Prometheus metrics (optional) ---
	if c.Prometheus != nil {
		metrics, err := c.Prometheus.QueryWorkload(ctx, deployment.Namespace, deployment.Name)
		if err != nil {
			logger.V(1).Info("prometheus query failed — continuing without metrics", "err", err)
		} else {
			obs.Metrics = metrics
		}
	}

	logger.Info("📦 ObservabilityContext collected",
		"pods", len(obs.Pods),
		"events", len(obs.KubeEvents),
		"hasMetrics", obs.Metrics != nil,
	)

	return obs, nil
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
