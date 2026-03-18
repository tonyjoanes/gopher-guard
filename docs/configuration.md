# Configuration Reference

## AegisWatch CRD fields

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `spec.targetRef.name` | string | — | ✅ | Name of the Deployment to watch |
| `spec.targetRef.namespace` | string | CR namespace | | Namespace of the Deployment (defaults to AegisWatch namespace) |
| `spec.llmModel` | string | `claude-haiku-4-5-20251001` | | Anthropic model identifier |
| `spec.llmSecretRef` | string | — | ✅ | Secret name containing `apiKey` (Anthropic API key) |
| `spec.gitRepo` | string | — | ✅ | `"owner/repo"` — GitHub repo to open PRs against |
| `spec.gitSecretRef` | string | — | ✅ | Secret name with `token` key (GitHub PAT). Optional `webhookUrl` for Slack/Discord |
| `spec.safeMode` | bool | `false` | | When `true`, GopherGuard logs diagnosis but does NOT open PRs |
| `spec.restartThreshold` | int (≥1) | `3` | | Restart count that triggers anomaly detection |

## AegisWatch status fields

| Field | Description |
|-------|-------------|
| `status.phase` | `Watching` → `Degraded` → `Healing` → `Healthy` |
| `status.lastDiagnosis` | LLM root cause + witty line from last healing cycle |
| `status.lastPRUrl` | URL of the most recently opened healing PR |
| `status.healingScore` | Count of successfully created healing PRs |
| `status.lastAnomalyTime` | Timestamp of most recent anomaly detection |
| `status.conditions` | Standard Kubernetes conditions array |

## Helm values

Key values (see `charts/gopher-guard/values.yaml` for the full list):

| Value | Default | Description |
|-------|---------|-------------|
| `replicaCount` | `2` | Operator replicas (HA with leader election) |
| `image.repository` | `ghcr.io/tonyjoanes/gopher-guard` | Container image repository |
| `image.tag` | chart appVersion | Image tag |
| `leaderElection` | `true` | Enable leader election (required for replicaCount > 1) |
| `prometheusURL` | `""` | Prometheus URL for workload metrics (e.g. `http://prometheus:9090`) |
| `metrics.enabled` | `true` | Expose operator Prometheus metrics |
| `metrics.secure` | `true` | Serve metrics over HTTPS |
| `resources.limits.memory` | `128Mi` | Operator memory limit |
| `installCRDs` | `true` | Install CRDs automatically |

## LLM provider

GopherGuard uses Anthropic (Claude) for diagnosis.

- Default model: `claude-haiku-4-5-20251001`
- Recommended for best quality: `claude-sonnet-4-6`
- Get an API key at [console.anthropic.com](https://console.anthropic.com)

```bash
kubectl create secret generic anthropic-api-key \
  --from-literal=apiKey=sk-ant-... \
  -n gopher-guard-system
```

Example AegisWatch spec:
```yaml
llmModel: claude-haiku-4-5-20251001
llmSecretRef: anthropic-api-key
```

## Notification webhooks

Store the webhook URL in the `gitSecretRef` secret under key `webhookUrl`:

```bash
kubectl create secret generic github-token \
  --from-literal=token=ghp_... \
  --from-literal=webhookUrl=https://hooks.slack.com/services/...
```

GopherGuard auto-detects Discord URLs (contains `discord.com`) and formats accordingly.

## Deployment manifest discovery

GopherGuard looks for the deployment manifest in the GitHub repo in this order:

1. `deploy/{deployment}/deployment.yaml`
2. `deploy/{deployment}/deployment.yml`
3. `manifests/{deployment}/deployment.yaml`
4. `manifests/{deployment}.yaml`
5. `k8s/{deployment}/deployment.yaml`

If your repo uses a different layout, the manifest will not be found and no PR will be created (diagnosis is still logged).
