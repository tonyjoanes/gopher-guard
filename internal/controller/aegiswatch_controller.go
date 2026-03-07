/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	opsv1alpha1 "github.com/tonyjoanes/gopher-guard/api/v1alpha1"
	ggithub "github.com/tonyjoanes/gopher-guard/internal/github"
	ggk8s "github.com/tonyjoanes/gopher-guard/internal/k8s"
	"github.com/tonyjoanes/gopher-guard/internal/llm"
	"github.com/tonyjoanes/gopher-guard/internal/notify"
	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

const (
	// reasonCrashLoopBackOff is the Kubernetes container waiting reason for crash loops.
	reasonCrashLoopBackOff = "CrashLoopBackOff"
	// reasonOOMKilled is the Kubernetes termination reason for out-of-memory kills.
	reasonOOMKilled = "OOMKilled"

	// finalizer is registered on every AegisWatch to allow cleanup on deletion.
	finalizer = "gopherguard.ops.gopherguard.dev/cleanup"
)

const requeueInterval = 30 * time.Second

// gopherArt is printed whenever GopherGuard delivers an AI diagnosis.
const gopherArt = `
    /\_____/\
   /  o   o  \   G O P H E R G U A R D
  ( ==  ^  == )  ─────────────────────────
   )         (   AI Diagnosis:
  (           )
 ( (  )   (  ) )
(__(__)___(__)__)
`

// AegisWatchReconciler reconciles an AegisWatch object.
type AegisWatchReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	Collector *observability.Collector
}

// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile is the main control loop.
//
//  1. Fetch AegisWatch CR + resolve target Deployment.
//  2. Detect anomaly (CrashLoopBackOff / OOMKilled / restarts / unavailable).
//  3. On anomaly:
//     a. Collect ObservabilityContext.
//     b. Set phase = Healing.
//     c. Call LLM → Diagnosis.
//     d. If !safeMode && patch != "" && healingScore < max: open GitHub PR.
//     e. Increment healingScore, store lastDiagnosis + lastPRUrl in status.
//     f. Send Slack/Discord notification.
//  4. On healthy: set phase = Healthy.
func (r *AegisWatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// --- 1. Fetch AegisWatch CR ---
	var watch opsv1alpha1.AegisWatch
	if err := r.Get(ctx, req.NamespacedName, &watch); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching AegisWatch: %w", err)
	}

	logger.Info("👀 GopherGuard sees you!",
		"aegiswatch", req.NamespacedName,
		"target", watch.Spec.TargetRef.Name,
		"phase", watch.Status.Phase,
	)

	// --- Handle deletion ---
	if !watch.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &watch)
	}

	// Ensure our finalizer is registered so cleanup runs on deletion.
	if !containsString(watch.Finalizers, finalizer) {
		watch.Finalizers = append(watch.Finalizers, finalizer)
		if err := r.Update(ctx, &watch); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil // requeue after update
	}

	// --- 2. Resolve target namespace + fetch Deployment ---
	targetNS := resolveTargetNamespace(watch.Spec.TargetRef.Namespace, watch.Namespace)

	var deployment appsv1.Deployment
	deployKey := types.NamespacedName{Name: watch.Spec.TargetRef.Name, Namespace: targetNS}
	if err := r.Get(ctx, deployKey, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Target Deployment not found — will retry", "deployment", deployKey)
			r.Recorder.Event(&watch, corev1.EventTypeWarning, "DeploymentNotFound",
				fmt.Sprintf("Deployment %s not found in namespace %s", deployKey.Name, deployKey.Namespace))
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching Deployment %s: %w", deployKey, err)
	}

	// --- 3. Detect anomaly ---
	threshold := watch.Spec.RestartThreshold
	if threshold == 0 {
		threshold = 3
	}
	anomaly, reason, err := detectAnomaly(ctx, r.Client, &deployment, threshold)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("detecting anomaly: %w", err)
	}

	if !anomaly {
		return r.setHealthy(ctx, req, &watch)
	}

	// --- Anomaly path ---
	logger.Info("🚨 Houston, we have a problem!",
		"deployment", deployKey, "reason", reason)
	r.Recorder.Event(&watch, corev1.EventTypeWarning, "AnomalyDetected", reason)

	// Anti-loop guard: skip LLM+PR if we've already hit the cap.
	if watch.Status.HealingScore >= ggithub.MaxHealingAttempts {
		logger.Info("⛔ MaxHealingAttempts reached — not opening another PR",
			"score", watch.Status.HealingScore,
			"max", ggithub.MaxHealingAttempts,
		)
		r.Recorder.Event(&watch, corev1.EventTypeWarning, "HealingCapReached",
			fmt.Sprintf("HealingScore %d >= MaxHealingAttempts %d; manual intervention required",
				watch.Status.HealingScore, ggithub.MaxHealingAttempts))
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// --- 4a. Collect ObservabilityContext ---
	obs, err := r.Collector.Collect(ctx, &deployment, reason)
	if err != nil {
		logger.Error(err, "failed to collect observability context")
		obs = &observability.ObservabilityContext{
			DeploymentName: deployment.Name,
			Namespace:      deployment.Namespace,
			AnomalyReason:  reason,
			CollectedAt:    time.Now(),
		}
	} else {
		logger.Info(obs.Summary())
	}

	// --- 4b. Set phase = Healing before slow LLM call ---
	now := metav1.Now()
	if err := r.patchStatus(ctx, req, func(w *opsv1alpha1.AegisWatch) {
		w.Status.Phase = opsv1alpha1.PhaseHealing
		w.Status.LastAnomalyTime = &now
	}); err != nil {
		return ctrl.Result{}, err
	}

	// --- 4c. LLM diagnosis ---
	diagnosis, diagErr := r.runDiagnosis(ctx, &watch, obs)
	if diagErr != nil {
		logger.Error(diagErr, "LLM diagnosis failed")
		r.Recorder.Event(&watch, corev1.EventTypeWarning, "DiagnosisFailed", diagErr.Error())
		_ = r.patchStatus(ctx, req, func(w *opsv1alpha1.AegisWatch) {
			w.Status.Phase = opsv1alpha1.PhaseDegraded
		})
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// Print gopher art + diagnosis to operator stdout.
	logger.Info(gopherArt +
		"Root cause : " + diagnosis.RootCause + "\n" +
		"Witty line : " + diagnosis.WittyLine + "\n" +
		"YAML patch : " + yesNo(diagnosis.YAMLPatch != "") + "\n")

	r.Recorder.Event(&watch, corev1.EventTypeNormal, "DiagnosisComplete",
		fmt.Sprintf("AI diagnosis: %s", diagnosis.RootCause))

	// --- 4d. Read git secret once (used by both PR creation and notification) ---
	gitToken, webhookURL := r.readGitSecrets(ctx, &watch)

	// --- 4e. Open GitHub PR ---
	var prURL string
	if err := r.maybeOpenPR(ctx, &watch, &deployment, diagnosis, targetNS, gitToken, &prURL); err != nil {
		// PR failure is non-fatal: log + notify but don't fail the reconcile.
		logger.Error(err, "failed to create healing PR")
		r.Recorder.Event(&watch, corev1.EventTypeWarning, "PRCreationFailed", err.Error())
	}

	// --- 4f. Update status ---
	diagSummary := fmt.Sprintf("%s\n\n💬 %s", diagnosis.RootCause, diagnosis.WittyLine)
	if err := r.patchStatus(ctx, req, func(w *opsv1alpha1.AegisWatch) {
		w.Status.Phase = opsv1alpha1.PhaseDegraded // stays until PR is merged
		w.Status.LastDiagnosis = diagSummary
		if prURL != "" {
			w.Status.LastPRURL = prURL
			w.Status.HealingScore++
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	// --- 4g. Send webhook notification ---
	if err := r.sendNotification(ctx, &watch, diagnosis, prURL, targetNS, webhookURL); err != nil {
		logger.Error(err, "webhook notification failed (non-fatal)")
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// setHealthy patches the AegisWatch status to Healthy and requeues.
func (r *AegisWatchReconciler) setHealthy(ctx context.Context, req ctrl.Request, watch *opsv1alpha1.AegisWatch) (ctrl.Result, error) {
	if err := r.patchStatus(ctx, req, func(w *opsv1alpha1.AegisWatch) {
		w.Status.Phase = opsv1alpha1.PhaseHealthy
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// patchStatus re-fetches the CR, applies mutFn, and patches .status.
func (r *AegisWatchReconciler) patchStatus(
	ctx context.Context,
	req ctrl.Request,
	mutFn func(*opsv1alpha1.AegisWatch),
) error {
	var watch opsv1alpha1.AegisWatch
	if err := r.Get(ctx, req.NamespacedName, &watch); err != nil {
		return fmt.Errorf("re-fetching AegisWatch for status patch: %w", err)
	}
	patch := client.MergeFrom(watch.DeepCopy())
	mutFn(&watch)
	if err := r.Status().Patch(ctx, &watch, patch); err != nil {
		return fmt.Errorf("patching AegisWatch status: %w", err)
	}
	return nil
}

// runDiagnosis builds the correct LLM client and calls Diagnose.
func (r *AegisWatchReconciler) runDiagnosis(
	ctx context.Context,
	watch *opsv1alpha1.AegisWatch,
	obs *observability.ObservabilityContext,
) (*llm.Diagnosis, error) {
	llmClient, err := llm.NewFromSpec(ctx, r.Client, watch)
	if err != nil {
		return nil, fmt.Errorf("building LLM client: %w", err)
	}
	return llmClient.Diagnose(ctx, obs)
}

// readGitSecrets reads the GitHub token and optional webhook URL from the
// gitSecretRef Secret in a single API call each. Results are returned as plain
// strings so callers never touch the Secret directly.
func (r *AegisWatchReconciler) readGitSecrets(ctx context.Context, watch *opsv1alpha1.AegisWatch) (token, webhookURL string) {
	if watch.Spec.GitSecretRef == "" {
		return "", ""
	}
	t, err := ggk8s.ReadSecretKey(ctx, r.Client, watch.Namespace, watch.Spec.GitSecretRef, "token")
	if err == nil {
		token = t
	}
	// "webhookUrl" is optional — ignore errors.
	u, err := ggk8s.ReadSecretKey(ctx, r.Client, watch.Namespace, watch.Spec.GitSecretRef, "webhookUrl")
	if err == nil {
		webhookURL = u
	}
	return token, webhookURL
}

// maybeOpenPR creates a GitHub PR if conditions are met.
// prURL is set to the created PR URL on success.
func (r *AegisWatchReconciler) maybeOpenPR(
	ctx context.Context,
	watch *opsv1alpha1.AegisWatch,
	deployment *appsv1.Deployment,
	diagnosis *llm.Diagnosis,
	targetNS, githubToken string,
	prURL *string,
) error {
	logger := log.FromContext(ctx)

	if watch.Spec.SafeMode {
		logger.Info("🔒 safeMode=true — skipping PR creation, diagnosis logged only")
		return nil
	}
	if diagnosis.YAMLPatch == "" {
		logger.Info("LLM returned no YAML patch — skipping PR")
		return nil
	}
	if githubToken == "" {
		return fmt.Errorf("GitHub token unavailable (check secret %q)", watch.Spec.GitSecretRef)
	}

	owner, repo, err := ggithub.SplitRepo(watch.Spec.GitRepo)
	if err != nil {
		return err
	}

	ghClient := ggithub.NewGitHubPRClient(ctx, githubToken)
	result, err := ghClient.CreateHealingPR(ctx, ggithub.PRRequest{
		Owner:          owner,
		Repo:           repo,
		DeploymentName: deployment.Name,
		Namespace:      targetNS,
		Diagnosis:      diagnosis,
		HealingScore:   watch.Status.HealingScore,
	})
	if err != nil {
		return err
	}

	*prURL = result.PRURL
	logger.Info("✅ Healing PR created", "url", result.PRURL, "branch", result.BranchName)
	r.Recorder.Event(watch, corev1.EventTypeNormal, "PRCreated",
		fmt.Sprintf("Healing PR opened: %s", result.PRURL))
	return nil
}

// sendNotification fires a Slack/Discord webhook if webhookURL is non-empty.
func (r *AegisWatchReconciler) sendNotification(
	ctx context.Context,
	watch *opsv1alpha1.AegisWatch,
	diagnosis *llm.Diagnosis,
	prURL, targetNS, webhookURL string,
) error {
	notifier := notify.NewNotificationClient(webhookURL)
	return notifier.SendHealingUpdate(ctx, notify.HealingUpdate{
		DeploymentName: watch.Spec.TargetRef.Name,
		Namespace:      targetNS,
		Diagnosis:      diagnosis,
		PRURL:          prURL,
		HealingScore:   watch.Status.HealingScore + 1,
		SafeMode:       watch.Spec.SafeMode,
	})
}

// resolveTargetNamespace returns targetNS if non-empty, otherwise falls back to defaultNS.
func resolveTargetNamespace(targetNS, defaultNS string) string {
	if targetNS != "" {
		return targetNS
	}
	return defaultNS
}

// detectAnomaly scans pods for known failure conditions.
func detectAnomaly(
	ctx context.Context,
	c client.Client,
	deployment *appsv1.Deployment,
	restartThreshold int32,
) (bool, string, error) {
	selector, err := selectorFromDeployment(deployment)
	if err != nil {
		return false, "", err
	}

	var podList corev1.PodList
	if err := c.List(ctx, &podList,
		client.InNamespace(deployment.Namespace),
		client.MatchingLabels(selector),
	); err != nil {
		return false, "", fmt.Errorf("listing pods: %w", err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.RestartCount >= restartThreshold {
				return true, fmt.Sprintf(
					"pod %s/%s container %q has restarted %d times",
					pod.Namespace, pod.Name, cs.Name, cs.RestartCount,
				), nil
			}
			if w := cs.State.Waiting; w != nil {
				if w.Reason == reasonCrashLoopBackOff || w.Reason == reasonOOMKilled {
					return true, fmt.Sprintf(
						"pod %s/%s container %q is in %s",
						pod.Namespace, pod.Name, cs.Name, w.Reason,
					), nil
				}
			}
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == reasonOOMKilled {
				return true, fmt.Sprintf(
					"pod %s/%s container %q was OOMKilled",
					pod.Namespace, pod.Name, cs.Name,
				), nil
			}
		}
	}

	if deployment.Status.UnavailableReplicas > 0 {
		return true, fmt.Sprintf(
			"deployment %s/%s has %d unavailable replicas",
			deployment.Namespace, deployment.Name,
			deployment.Status.UnavailableReplicas,
		), nil
	}

	return false, "", nil
}

func selectorFromDeployment(d *appsv1.Deployment) (map[string]string, error) {
	if d.Spec.Selector == nil || len(d.Spec.Selector.MatchLabels) == 0 {
		return nil, fmt.Errorf("deployment %s/%s has no matchLabels selector", d.Namespace, d.Name)
	}
	return d.Spec.Selector.MatchLabels, nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// handleDeletion runs cleanup when an AegisWatch is being deleted, then
// removes the finalizer so Kubernetes can garbage-collect the object.
func (r *AegisWatchReconciler) handleDeletion(ctx context.Context, watch *opsv1alpha1.AegisWatch) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("🧹 AegisWatch deleted — running cleanup", "name", watch.Name)

	// Close any open healing PR if we know its URL.
	if watch.Status.LastPRURL != "" && watch.Spec.GitSecretRef != "" {
		token, _ := r.readGitSecrets(ctx, watch)
		if token != "" {
			owner, repo, err := ggithub.SplitRepo(watch.Spec.GitRepo)
			if err == nil {
				ghClient := ggithub.NewGitHubPRClient(ctx, token)
				if closeErr := ghClient.CloseHealingPR(ctx, owner, repo, watch.Status.LastPRURL); closeErr != nil {
					logger.Error(closeErr, "could not close healing PR on deletion (non-fatal)", "url", watch.Status.LastPRURL)
				} else {
					logger.Info("✅ Closed healing PR", "url", watch.Status.LastPRURL)
				}
			}
		}
	}

	// Remove finalizer — allows Kubernetes to delete the object.
	watch.Finalizers = removeString(watch.Finalizers, finalizer)
	if err := r.Update(ctx, watch); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// SetupWithManager wires the reconciler into the controller-runtime manager.
func (r *AegisWatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	podToAegisWatches := func(ctx context.Context, obj client.Object) []reconcile.Request {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil
		}
		var watchList opsv1alpha1.AegisWatchList
		if err := r.List(ctx, &watchList); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, aw := range watchList.Items {
			if resolveTargetNamespace(aw.Spec.TargetRef.Namespace, aw.Namespace) == pod.Namespace {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: aw.Namespace,
						Name:      aw.Name,
					},
				})
			}
		}
		return requests
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.AegisWatch{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(podToAegisWatches),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("aegiswatch").
		Complete(r)
}
