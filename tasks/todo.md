# GopherGuard — Task List

**Project**: AI-Powered Self-Healing GitOps Guardian (Go Kubernetes Operator)
**Goal**: Watch GitOps apps, detect anomalies, ask LLM for fix, auto-open PR, ArgoCD/Flux applies it.

---

## Phase 1 — Foundation & Local Environment

### 1.1 Local cluster & tooling
- [ ] Install prerequisites: `go 1.23+`, `kind`, `kubectl`, `kubebuilder`, `tilt`, `helm`
- [ ] Create local `kind` cluster (`kind create cluster --name gopherguard`)
- [ ] Verify `kubectl` context points to the kind cluster

### 1.2 GitOps bootstrap
- [ ] Choose and bootstrap ArgoCD **or** Flux v2 from a new Git repo
  - ArgoCD: `kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml`
  - Flux: `flux bootstrap github --owner=<you> --repository=gopherguard --branch=main`
- [ ] Confirm GitOps controller is syncing the repo

### 1.3 JokeService demo app
- [x] Write a minimal Go HTTP server (`cmd/jokeservice/main.go`) that:
  - Randomly returns HTTP 500 (`~20% of requests`)
  - Randomly panics / OOMs to simulate a crash loop
- [x] Write `Dockerfile` for JokeService (`deploy/jokeservice/Dockerfile`)
- [x] Write Kubernetes manifests: `Deployment`, `Service` under `deploy/jokeservice/`
- [x] Add ArgoCD `Application` YAML to sync JokeService from Git (`deploy/argocd/jokeservice-app.yaml`)
- [ ] **[YOU]** Build & push image: `docker build -f deploy/jokeservice/Dockerfile -t ghcr.io/<you>/jokeservice:latest . && docker push`
- [ ] **[YOU]** Deploy JokeService via GitOps: `kubectl apply -f deploy/argocd/jokeservice-app.yaml`
- [ ] **[YOU]** Confirm crash-looping: `kubectl get pods -n demo -w`

### 1.4 Operator skeleton (Kubebuilder)
- [x] Run `kubebuilder init --domain gopherguard.dev --repo github.com/tonyjoanes/gopher-guard`
- [x] Run `kubebuilder create api --group ops --version v1alpha1 --kind AegisWatch`
- [x] Define `AegisWatchSpec` fields (targetRef, llmProvider, llmModel, gitRepo, safeMode, restartThreshold)
- [x] Define `AegisWatchStatus` fields (phase, lastDiagnosis, lastPRUrl, healingScore, lastAnomalyTime, conditions)
- [x] `make generate && make manifests` — CRD YAML generated
- [x] Write reconciler with anomaly detection (CrashLoopBackOff, OOMKilled, restart threshold, unavailable replicas)
- [ ] **[YOU]** `make install` — install CRDs into your kind cluster
- [ ] **[YOU]** `make run` — run the operator locally and apply `config/samples/ops_v1alpha1_aegiswatch.yaml`

**Milestone 1**: Operator prints event in real-time when CR appears. ✓

---

## Phase 2 — Kubernetes Observability

### 2.1 Watch Pods & Deployments
- [x] Add `Watches(&corev1.Pod{}, ...)` in `SetupWithManager` — pod changes trigger reconcile
- [x] Detect unhealthy conditions:
  - `CrashLoopBackOff` (waiting state)
  - `OOMKilled` (waiting + last-terminated state)
  - Repeated restarts (restart count ≥ threshold)
  - Deployment `unavailableReplicas > 0`
- [x] Emit Kubernetes `Warning` Event on the `AegisWatch` CR when anomaly detected
- [x] Update `AegisWatchStatus.phase` to `Degraded`; record `lastAnomalyTime`

### 2.2 Log & event collection
- [x] Fetch last 50 lines of logs per container via raw `kubernetes.Clientset` (`PodLogOptions`) — with previous-container fallback (`internal/observability/logs.go`)
- [x] Fetch recent Kubernetes Warning events for the Deployment namespace, filtered by involved object names (`internal/observability/events.go`)
- [x] Package logs + events into structured `ObservabilityContext` Go struct (`internal/observability/context.go`)
- [x] `Collector.Collect()` orchestrates all sources into one context (`internal/observability/collector.go`)

### 2.3 Prometheus metrics
- [x] Prometheus HTTP query client (`internal/observability/prometheus.go`) — queries CPU (millicores) and memory (MiB) via instant query API
- [x] Metrics snapshot attached to `ObservabilityContext.Metrics`
- [x] Disabled gracefully when `--prometheus-url` flag is empty
- [x] Operator's own `/metrics` endpoint exposed via `controller-runtime` (kubebuilder default)

**Milestone 2**: Operator prints "Houston, we have a crashing pod" with full logs + event context. ✓

---

## Phase 3 — AIOps / LLM Integration

### 3.1 LLM client abstraction
- [x] Define `LLMClient` interface (`internal/llm/client.go`) + `Diagnosis` struct (`RootCause`, `YAMLPatch`, `WittyLine`)
- [x] Implement `GroqClient` — OpenAI-compatible REST (`internal/llm/groq.go`), `json_object` response format, 60s timeout, 2-attempt retry
- [x] Implement `OllamaClient` — Ollama `/api/chat` with `format: json` (`internal/llm/ollama.go`), 3-min timeout for slow local models
- [x] `NewFromSpec()` factory (`internal/llm/factory.go`) — reads API key from K8s Secret, selects provider from `spec.llmProvider`, defaults model per provider

### 3.2 Prompt engineering
- [x] System prompt (`internal/llm/prompt.go`): strict JSON schema, patch rules (no image changes, only resources/env/probes/replicas), witty line constraints
- [x] `BuildUserPrompt()`: structured markdown with deployment metadata, Prometheus metrics, per-pod/container state, log tails (capped at 3000 chars), K8s events table
- [x] `parseDiagnosis()`: JSON unmarshal → `Diagnosis`; validates non-empty `rootCause`
- [x] Retry once on transient LLM failure; error stored as K8s Event on CR

### 3.3 Reconciler integration
- [x] Phase transitions: Degraded → **Healing** (before LLM call) → Degraded (after, until Phase 4 PR merge)
- [x] `runDiagnosis()` builds LLM client per-reconcile from live CR spec + secret
- [x] `lastDiagnosis` in status: `"<rootCause>\n\n💬 <wittyLine>"`
- [x] K8s Event `DiagnosisComplete` emitted on success; `DiagnosisFailed` on error
- [x] Gopher ASCII art + root cause + witty line + patch indicator logged to stdout
- [x] `_ = diagnosis.YAMLPatch` Phase 4 hook ready for PR creation

**Milestone 3**: Operator outputs "AI says: add memory limit 256Mi — *This pod crashed harder than my hopes for Monday.*" ✓

---

## Phase 4 — GitOps Loop (Auto-PR)

### 4.1 GitHub PR creation
- [x] Add `github.com/google/go-github/v62` dependency
- [x] `GitHubPRClient` (`internal/github/client.go`):
  - Searches conventional paths (`deploy/<name>/deployment.yaml`, `manifests/<name>.yaml`, etc.)
  - Reads file via GitHub Contents API (no git clone needed)
  - `ApplyYAMLPatch()` — strategic merge patch via `k8s.io/apimachinery/pkg/util/strategicpatch` (`internal/github/patch.go`)
  - Creates branch `gopherguard/fix-<deployment>-<unix-ts>`
  - Commits patched file with structured message
  - Opens PR: title `fix(<deployment>): AI-suggested remediation [GopherGuard #N]`, body with root cause + YAML diff + witty line
- [x] `lastPRUrl` stored in `AegisWatchStatus` after each successful PR
- [x] `safeMode: true` skips PR entirely — logs diagnosis only

### 4.2 Slack/Discord webhook
- [x] `NotificationClient` (`internal/notify/webhook.go`) — auto-detects Slack vs Discord from URL
- [x] Slack: Block Kit payload with header, details, PR link sections
- [x] Discord: Embed with colour (green=success, yellow=safeMode), fields, footer
- [x] Webhook URL read from optional `"webhookUrl"` key in the `gitSecretRef` Secret (zero config when absent)

### 4.3 Healing score
- [x] `healingScore` incremented in status after each successful PR creation
- [x] Anti-loop guard: `MaxHealingAttempts = 5` — if score ≥ cap, emits `HealingCapReached` Warning Event and skips LLM/PR; requires manual intervention

**Milestone 4**: Trigger chaos → PR appears automatically in GitHub → merge → ArgoCD/Flux applies fix → pod healthy. Record the 60-second demo. ✓

---

## Phase 5 — GopherGuard Self-Manages via GitOps (Meta!)

- [ ] Write Helm chart **or** Kustomize manifests for GopherGuard itself under `deploy/gopherguard/`
  - `CRDs`, `Deployment`, `ServiceAccount`, `ClusterRole`, `ClusterRoleBinding`, `Secret` template
- [ ] Add ArgoCD `Application` (or Flux `Kustomization`) that syncs GopherGuard from Git
- [ ] Deploy GopherGuard to the cluster entirely through GitOps (no `make run` in production)
- [ ] Verify operator self-heals if its own deployment is accidentally deleted by ArgoCD re-sync

**Milestone 5**: GopherGuard is deployed and managed by the same GitOps system it guards. ✓

---

## Phase 6 — Dashboard (HTMX)

- [ ] Add `github.com/labstack/echo/v4` (or `github.com/gofiber/fiber/v2`) dependency
- [ ] Create `cmd/dashboard/main.go` — embeds a simple HTTP server
- [ ] Design minimal HTMX UI pages:
  - `/` — list of `AegisWatch` CRs with current `phase` badge
  - `/watch/<name>` — healing timeline (last 10 interventions), `healingScore`, last PR link, witty quotes
  - `/events` — SSE stream of real-time operator events (HTMX `hx-trigger="every 2s"`)
- [ ] Serve from inside the operator binary (single binary) or as a sidecar
- [ ] Add `HealingEvent` type stored in-memory (or ConfigMap) for timeline

**Milestone 6**: Open browser, see live healing events and score without any JS framework. ✓

---

## Phase 7 — Polish & Extensions

### 7.1 ArgoCD + Flux dual support
- [ ] Watch `argoproj.io/v1alpha1/Application` resources for `SyncFailed` / `Degraded` health
- [ ] Watch `kustomize.toolkit.fluxcd.io/v1/Kustomization` for `False` Ready condition
- [ ] Route anomalies from either controller into the same `ObservabilityContext` pipeline

### 7.2 Multi-cluster (stretch goal)
- [ ] Support ArgoCD `ApplicationSets` targeting multiple clusters
- [ ] Use kubeconfig secrets or cluster secrets to reach remote clusters

### 7.3 Offline / local LLM mode
- [ ] Validate full flow with Ollama + `llama3` running locally (no internet required)
- [ ] Document setup in `docs/offline-mode.md`

### 7.4 Safe-mode UX
- [ ] When `safeMode: true`, post diagnosis as GitHub **Issue comment** instead of PR
- [ ] Add `--dry-run` flag to operator for CI/local testing

---

## Cross-cutting / Always-on Tasks

- [ ] **Tests**: Unit tests for `LLMClient`, `GitHubPRClient`, reconciler state machine (use `envtest`)
- [ ] **CI**: GitHub Actions workflow — `go vet`, `staticcheck`, `go test ./...`, `docker build`
- [ ] **Security**: All secrets (GitHub token, LLM API key, webhook URL) via K8s `Secret` + env vars
- [ ] **Docs**: `docs/architecture.md` — one-page architecture diagram (ASCII or Mermaid)
- [ ] **Demo**: Record 60-second screen capture of full chaos → PR → fix loop
- [ ] **lessons.md**: Keep `tasks/lessons.md` updated after every non-trivial correction

---

## Current Status

| Phase | Status |
|-------|--------|
| 1 — Foundation | ✅ Done |
| 2 — Observability | ✅ Done |
| 3 — LLM Integration | ✅ Done |
| 4 — GitOps Loop / Auto-PR | ✅ Done |
| 5 — Self-managed via GitOps | ⬜ Not started |
| 6 — HTMX Dashboard | ⬜ Not started |
| 7 — Polish & Extensions | ⬜ Not started |
