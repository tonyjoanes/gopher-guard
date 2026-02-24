package llm

import (
	"fmt"
	"strings"
	"time"

	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

// SystemPrompt is sent as the LLM system message on every request.
// It instructs the model to respond with a strict JSON schema and sets
// clear boundaries on what the YAML patch may and may not change.
const SystemPrompt = `You are GopherGuard, an expert Kubernetes SRE AI assistant embedded inside a self-healing GitOps operator written in Go.

Your job is to analyse observability data from a broken Kubernetes workload and return a concise diagnosis plus a minimal, safe remediation patch.

RESPONSE FORMAT
You MUST respond with ONLY valid JSON — no markdown fences, no explanation outside the JSON object:
{
  "rootCause": "<1-2 sentence technical root cause>",
  "patch":     "<minimal Kubernetes strategic-merge-patch YAML for the Deployment spec, or empty string>",
  "wittyLine": "<one witty gopher/Go-themed sentence about the failure>"
}

PATCH RULES (strictly enforced)
- Only suggest changes to: resources.limits, resources.requests, env, args, livenessProbe, readinessProbe, replicas
- NEVER change the image, imagePullPolicy, or any field outside spec.template.spec.containers[] / spec.replicas
- If the root cause cannot be fixed by a Deployment patch, return an empty string for "patch"
- YAML patch must be valid and apply cleanly with kubectl apply --server-side

WITTY LINE RULES
- Reference Go gophers, Kubernetes, cloud-native culture, or general programming humour
- Keep it under 120 characters
- Do not use profanity
`

// BuildUserPrompt assembles the user-turn message from an ObservabilityContext.
// The output is structured markdown so the LLM can scan sections quickly.
func BuildUserPrompt(obs *observability.ObservabilityContext) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Workload Under Investigation\n")
	fmt.Fprintf(&sb, "- **Deployment**: `%s/%s`\n", obs.Namespace, obs.DeploymentName)
	fmt.Fprintf(&sb, "- **Anomaly detected**: %s\n", obs.AnomalyReason)
	fmt.Fprintf(&sb, "- **Snapshot time**: %s\n\n", obs.CollectedAt.UTC().Format(time.RFC3339))

	// --- Metrics ---
	if obs.Metrics != nil {
		fmt.Fprintf(&sb, "## Resource Usage (Prometheus)\n")
		fmt.Fprintf(&sb, "- CPU: %.2f millicores\n", obs.Metrics.CPUUsageMillicores)
		fmt.Fprintf(&sb, "- Memory: %.2f MiB\n\n", obs.Metrics.MemUsageMiB)
	}

	// --- Pods ---
	fmt.Fprintf(&sb, "## Pod States (%d pods)\n", len(obs.Pods))
	for _, pod := range obs.Pods {
		fmt.Fprintf(&sb, "\n### Pod `%s` — phase: %s", pod.Name, pod.Phase)
		if pod.NodeName != "" {
			fmt.Fprintf(&sb, " (node: %s)", pod.NodeName)
		}
		fmt.Fprintf(&sb, "\n")
		for _, c := range pod.Containers {
			fmt.Fprintf(&sb, "#### Container `%s`\n", c.Name)
			fmt.Fprintf(&sb, "- Image: `%s`\n", c.Image)
			fmt.Fprintf(&sb, "- State: `%s`\n", c.State)
			fmt.Fprintf(&sb, "- Restart count: %d\n", c.RestartCount)
			if c.LastLogs != "" {
				fmt.Fprintf(&sb, "\n**Recent logs (last 50 lines)**:\n```\n%s\n```\n", trimLogs(c.LastLogs, 3000))
			}
		}
	}

	// --- Kubernetes Events ---
	if len(obs.KubeEvents) > 0 {
		fmt.Fprintf(&sb, "\n## Kubernetes Events (most recent %d)\n", len(obs.KubeEvents))
		fmt.Fprintf(&sb, "| Time | Type | Reason | Object | Message |\n")
		fmt.Fprintf(&sb, "|------|------|--------|--------|---------|\n")
		for _, ev := range obs.KubeEvents {
			msg := ev.Message
			if len(msg) > 120 {
				msg = msg[:117] + "..."
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
				ev.LastSeen.UTC().Format("15:04:05"),
				ev.Type,
				ev.Reason,
				ev.InvolvedObject,
				msg,
			)
		}
	}

	fmt.Fprintf(&sb, "\n---\nDiagnose the root cause and provide a safe remediation patch following the rules in your system prompt.\n")

	return sb.String()
}

// trimLogs caps log content to maxChars to stay within LLM context limits.
func trimLogs(logs string, maxChars int) string {
	if len(logs) <= maxChars {
		return logs
	}
	// Keep the tail — the most recent lines are most useful.
	return "...[truncated]...\n" + logs[len(logs)-maxChars:]
}
