# GopherGuard – Phase-by-Phase Build Guide (2026 Edition)

**Project**: AI-powered self-healing GitOps guardian (Go + ArgoCD + Grok API)  
**Your repo**: `gopher-guard` (already created)  
**Goal**: Run everything locally on `kind`, watch it auto-create GitHub PRs when your app breaks.

## Phase 0: Export Your Keys (2 min)
You already have GitHub + Grok account, so just set the env vars:

```bash
export XAI_API_KEY="xai-..."          # from https://console.x.ai
export GITHUB_TOKEN="ghp_..."         # classic PAT with "repo" scope
```

Add them to `~/.zshrc` / `~/.bashrc` so they survive reboots.

**Success**: `echo $XAI_API_KEY` and `echo $GITHUB_TOKEN` show values.

## Phase 1: Local Cluster + ArgoCD (10 min)
```bash
kind create cluster --name gopherguard --config - <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 80
    hostPort: 8080
EOF

kubectl create ns argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n argocd wait --for=condition=available deploy/argocd-server --timeout=300s

ARGOCD_PW=$(kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d)
echo "ArgoCD UI → http://localhost:8080"
echo "user: admin   pass: $ARGOCD_PW"
kubectl port-forward -n argocd svc/argocd-server 8080:443 &
```

Open http://localhost:8080 and log in.

**Success**: ArgoCD dashboard is live.

## Phase 2: Create the Joke-Service Repo (5 min)
Create a new public GitHub repo: `YOUR-USERNAME/joke-service`  
Clone it locally and add the files below (this is the app that will randomly crash).

```bash
# Inside joke-service repo
mkdir -p k8s cmd/joke

cat > cmd/joke/main.go <<'EOF'
package main
import ("fmt"; "math/rand"; "net/http"; "time")
func main() {
	rand.Seed(time.Now().UnixNano())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if rand.Float64() < 0.4 { panic("random crash!") }
		fmt.Fprint(w, "😂 JokeService v1.0 running")
	})
	http.ListenAndServe(":8080", nil)
}
EOF

cat > k8s/deployment.yaml <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: joke-service}
spec:
  replicas: 2
  selector: {matchLabels: {app: joke-service}}
  template:
    metadata: {labels: {app: joke-service}}
    spec:
      containers:
      - name: joke
        image: ghcr.io/YOUR-USERNAME/joke-service:latest
        ports: [{containerPort: 8080}]
        resources:
          limits: {memory: "128Mi", cpu: "100m"}
EOF

cat > k8s/service.yaml <<EOF
apiVersion: v1
kind: Service
metadata: {name: joke-service}
spec:
  selector: {app: joke-service}
  ports: [{port: 80, targetPort: 8080}]
EOF
```

Add a basic Dockerfile, commit & push.

## Phase 3: Initialize Operator Inside Your Existing Repo (10 min)
```bash
cd gopher-guard   # your existing repo
kubebuilder init --domain gopherguard.dev --repo github.com/YOUR-USERNAME/gopher-guard
kubebuilder create api --group ops --version v1alpha1 --kind AegisWatch --resource --controller
go mod tidy
```

Commit the generated skeleton.

## Phase 4: CRD & Basic Reconciler Skeleton (20 min)
Replace the generated files with the exact content from my earlier message (or ask Claude).  
Run:
```bash
make manifests
make install
make run   # ← runs locally against kind
```

Apply a sample `AegisWatch` CR that targets the `joke-service` namespace.

**Success**: Logs show “watching namespace…”

## Phase 5: Observability – Logs & Events (30 min)
Add pod listing, log fetching, event watching.

**Success**: Delete a joke-service pod → operator prints logs/events.

## Phase 6: Grok API Integration (AIOps brain) (25 min)
```bash
go get github.com/sashabaranov/go-openai
```

Add the OpenAI client with `BaseURL: "https://api.x.ai/v1"` and your key.  
Prompt Grok for diagnosis + safe YAML patch.

**Success**: Operator prints witty Grok diagnosis.

## Phase 7: Auto GitHub PR (the magic) (40 min)
```bash
go get github.com/google/go-github/v62
```

Create branch, commit patch to `joke-service` repo, open PR.

**Success**: Trigger chaos → PR appears in `joke-service` within 60s.

## Phase 8: Status Updates (20 min)
Update `AegisWatch` status with `LastDiagnosis`, `LastPRURL`, `HealingScore`.

## Phase 9: Meta – Deploy GopherGuard via ArgoCD (30 min)
Build & push your own operator image, then create an ArgoCD Application that deploys `gopher-guard` itself.

## Phase 10: Chaos Testing & Demo (20 min)
Write `demo.sh`, record the 60-second video (this is your portfolio star).

## Phase 11: Polish (optional)
- HTMX dashboard
- Safe-mode flag
- Flux support

---

**How to use with Claude**  
Copy **one phase** at a time and tell Claude:  
> “Implement Phase 6 exactly as described in BUILDING.md for the repo gopher-guard. Show every file and command. Use Go 1.23+ best practices.”
