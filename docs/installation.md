# Installation

## Prerequisites

- Kubernetes 1.25+
- `kubectl` configured for your cluster
- A Groq API key (free at groq.com) **or** a local Ollama instance
- A GitHub Personal Access Token with `repo` + `pull_requests` scopes

## Install with Helm (recommended)

```bash
helm install gopher-guard oci://ghcr.io/tonyjoanes/gopher-guard/charts/gopher-guard \
  --namespace gopher-guard-system \
  --create-namespace \
  --version 0.1.0
```

Check the operator is running:

```bash
kubectl get pods -n gopher-guard-system
```

## Install with Kustomize

```bash
kubectl apply -f https://github.com/tonyjoanes/gopher-guard/releases/latest/download/install.yaml
```

## Create secrets

### Groq API key

```bash
kubectl create secret generic groq-api-key \
  --from-literal=apiKey=<your-groq-api-key> \
  -n gopher-guard-system
```

### GitHub token (+ optional Slack/Discord webhook)

```bash
kubectl create secret generic github-token \
  --from-literal=token=<your-github-pat> \
  --from-literal=webhookUrl=<optional-slack-or-discord-webhook-url> \
  -n gopher-guard-system
```

## Create your first AegisWatch

```yaml
apiVersion: ops.gopherguard.dev/v1alpha1
kind: AegisWatch
metadata:
  name: my-app-watcher
  namespace: gopher-guard-system
spec:
  targetRef:
    name: my-deployment      # Deployment to watch
    namespace: default       # Namespace of the Deployment
  llmProvider: groq
  llmModel: llama3-70b-8192
  llmSecretRef: groq-api-key
  gitRepo: owner/my-repo
  gitSecretRef: github-token
  safeMode: false            # Set true to disable auto-PR (log-only)
  restartThreshold: 3        # Trigger after 3 container restarts
```

```bash
kubectl apply -f aegiswatch.yaml
```

Watch the healing lifecycle:

```bash
kubectl get aegiswatch -n gopher-guard-system -w
```

## Uninstall

```bash
# Remove all AegisWatch CRs first (triggers cleanup of open PRs).
kubectl delete aegiswatch --all -A

# Then uninstall the operator.
helm uninstall gopher-guard -n gopher-guard-system
```
