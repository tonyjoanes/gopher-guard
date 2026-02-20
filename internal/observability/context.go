// Package observability collects runtime signals (pod logs, Kubernetes events,
// Prometheus metrics) and packages them into an ObservabilityContext that is
// later fed to the LLM for root-cause diagnosis (Phase 3).
package observability

import (
	"fmt"
	"time"
)

// ObservabilityContext is the full picture of a troubled workload at a
// single point in time. It is the payload sent to the LLM in Phase 3.
type ObservabilityContext struct {
	// DeploymentName is the name of the monitored Deployment.
	DeploymentName string
	// Namespace is the namespace of the Deployment.
	Namespace string
	// AnomalyReason is the short human-readable reason detected by the controller.
	AnomalyReason string
	// CollectedAt is when the snapshot was taken.
	CollectedAt time.Time

	// Pods contains per-pod snapshots including container logs.
	Pods []PodSnapshot
	// KubeEvents contains recent Warning events for the Deployment namespace.
	KubeEvents []KubeEvent
	// Metrics is the Prometheus snapshot. Nil when Prometheus is unavailable.
	Metrics *PrometheusSnapshot
}

// PodSnapshot captures the observable state of one Pod.
type PodSnapshot struct {
	Name   string
	Phase  string
	NodeName string
	// Containers holds per-container detail and logs.
	Containers []ContainerSnapshot
}

// ContainerSnapshot captures one container's state and recent log tail.
type ContainerSnapshot struct {
	Name         string
	Image        string
	RestartCount int32
	// State is a compact description, e.g. "running", "waiting:CrashLoopBackOff",
	// "terminated:OOMKilled:exit137".
	State string
	// LastLogs holds the last LogTailLines lines from the container's stdout/stderr.
	// Empty when logs cannot be fetched (e.g. container not yet started).
	LastLogs string
}

// KubeEvent is a trimmed Kubernetes Event focused on what the LLM needs.
type KubeEvent struct {
	// Type is "Normal" or "Warning".
	Type    string
	Reason  string
	Message string
	Count   int32
	LastSeen time.Time
	// InvolvedObject is "Pod/my-pod" or "Deployment/my-deploy".
	InvolvedObject string
}

// PrometheusSnapshot is a lightweight CPU/memory reading for the target workload.
type PrometheusSnapshot struct {
	CPUUsageMillicores float64
	MemUsageMiB        float64
	// QueryTime is when the Prometheus query was executed.
	QueryTime time.Time
}

// Summary produces a compact multi-line string of the context suitable for
// log output or an abbreviated LLM prompt prefix.
func (o *ObservabilityContext) Summary() string {
	out := "=== ObservabilityContext ===\n"
	out += "Deployment : " + o.Namespace + "/" + o.DeploymentName + "\n"
	out += "Anomaly    : " + o.AnomalyReason + "\n"
	out += "Pods       : " + itoa(len(o.Pods)) + "\n"
	out += "KubeEvents : " + itoa(len(o.KubeEvents)) + "\n"
	if o.Metrics != nil {
		out += "CPU (m)    : " + ftoa(o.Metrics.CPUUsageMillicores) + "\n"
		out += "Mem (MiB)  : " + ftoa(o.Metrics.MemUsageMiB) + "\n"
	}
	return out
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func ftoa(f float64) string {
	return fmt.Sprintf("%.2f", f)
}
