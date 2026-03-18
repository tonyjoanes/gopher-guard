# Installation

## Prerequisites

- Kubernetes 1.25+
- `kubectl` configured for your cluster
- An Anthropic API key (from [console.anthropic.com](https://console.anthropic.com))
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

### Anthropic API key

```bash
kubectl create secret generic anthropic-api-key \
  --from-literal=apiKey=sk-ant-... \
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
  llmModel: claude-haiku-4-5-20251001
  llmSecretRef: anthropic-api-key
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

## GitOps with ArgoCD (optional)

ArgoCD closes the healing loop: when GopherGuard merges a healing PR, ArgoCD auto-applies the fixed manifest to the cluster.

### Install ArgoCD

```bash
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl wait --for=condition=available --timeout=180s deployment/argocd-server -n argocd
```

### Register the jokeservice Application

The Application manifest lives at [`argocd/jokeservice-app.yaml`](../argocd/jokeservice-app.yaml). Apply it:

```bash
kubectl apply -f argocd/jokeservice-app.yaml
```

This tells ArgoCD to watch `deploy/jokeservice/` on the `main` branch and auto-sync on changes. `selfHeal` is disabled so ArgoCD doesn't fight GopherGuard's intermediate state.

Verify it's healthy:

```bash
kubectl get application jokeservice -n argocd
# Expected: SYNC STATUS=Synced  HEALTH STATUS=Healthy
```

### Access the ArgoCD UI

```bash
# Get the initial admin password (one-time — change it after first login)
kubectl -n argocd get secret argocd-initial-admin-secret \
  -o jsonpath="{.data.password}" | base64 -d; echo

# Port-forward (run in a separate terminal)
kubectl port-forward svc/argocd-server -n argocd 8080:443
```

Browse to **https://localhost:8080** and log in as `admin`.

### Full healing loop

```
crash → GopherGuard detects → LLM diagnoses → healing PR opened
          ↓ (you review & merge the PR)
ArgoCD polls repo (~3 min) → detects diff → auto-applies fix → pod recovers
```

## Uninstall

```bash
# Remove all AegisWatch CRs first (triggers cleanup of open PRs).
kubectl delete aegiswatch --all -A

# Then uninstall the operator.
helm uninstall gopher-guard -n gopher-guard-system
```
