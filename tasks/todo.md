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
- [ ] Define `LLMClient` interface:
  ```go
  type LLMClient interface {
      Diagnose(ctx context.Context, obs ObservabilityContext) (Diagnosis, error)
  }
  ```
- [ ] Implement `GroqClient` (OpenAI-compatible REST, `net/http`)
- [ ] Implement `OllamaClient` (local, `net/http` to `localhost:11434`)
- [ ] Wire provider selection from `AegisWatchSpec.llmProvider`

### 3.2 Prompt engineering
- [ ] Write system prompt that instructs LLM to:
  - Return structured JSON: `{ "rootCause": "...", "patch": "...yaml...", "wittyLine": "..." }`
  - Keep YAML patch minimal and safe (no image tag changes without explicit permission)
- [ ] Build user prompt from `ObservabilityContext`: include resource YAML, logs, events, metrics
- [ ] Parse and validate LLM JSON response into `Diagnosis` struct:
  ```go
  type Diagnosis struct {
      RootCause string
      YAMLPatch string
      WittyLine string
  }
  ```
- [ ] Handle LLM errors gracefully (retry once, then update status with error)

### 3.3 Reconciler integration
- [ ] Call LLM when `phase == Degraded`
- [ ] Store `Diagnosis.RootCause` + `Diagnosis.WittyLine` in `AegisWatchStatus.lastDiagnosis`
- [ ] Log witty line to operator stdout with a gopher ASCII art prefix

**Milestone 3**: Operator outputs "AI says: add memory limit 256Mi — *This pod crashed harder than my hopes for Monday.*" ✓

---

## Phase 4 — GitOps Loop (Auto-PR)

### 4.1 GitHub PR creation
- [ ] Add `github.com/google/go-github/v62` dependency
- [ ] Implement `GitHubPRClient`:
  - Clone/read current file from repo via GitHub API (avoid full git clone)
  - Apply YAML patch (merge strategy: strategic merge or JSON patch)
  - Create branch `gopherguard/fix-<resource>-<timestamp>`
  - Commit patched file
  - Open PR with title: "fix(<resource>): AI-suggested remediation" and body including `WittyLine` + `RootCause`
- [ ] Store PR URL in `AegisWatchStatus.lastPRUrl`
- [ ] Guard behind `AegisWatchSpec.safeMode` flag (log-only when `true`)

### 4.2 Slack/Discord webhook (optional)
- [ ] Write `NotificationClient` with a `SendHealingUpdate(Diagnosis, PRUrl)` method
- [ ] Format message with emoji, witty line, and PR link
- [ ] Read webhook URL from Kubernetes `Secret` (never hardcode)

### 4.3 Healing score
- [ ] Increment `AegisWatchStatus.healingScore` after each successful PR
- [ ] Reset or flag when pod stays crashlooping after N PRs (avoid infinite loop)

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
| 3 — LLM Integration | ⬜ Not started |
| 4 — GitOps Loop / Auto-PR | ⬜ Not started |
| 5 — Self-managed via GitOps | ⬜ Not started |
| 6 — HTMX Dashboard | ⬜ Not started |
| 7 — Polish & Extensions | ⬜ Not started |
