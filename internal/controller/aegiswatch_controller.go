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
	"github.com/tonyjoanes/gopher-guard/internal/llm"
	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

const requeueInterval = 30 * time.Second

// gopherArt is printed to stdout whenever GopherGuard delivers an AI diagnosis.
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
//  1. Fetch the AegisWatch CR.
//  2. Resolve and fetch the target Deployment.
//  3. Scan pods for anomalies (CrashLoopBackOff, OOMKilled, excessive restarts,
//     unavailable replicas).
//  4. On anomaly:
//     a. Collect ObservabilityContext (logs + events + metrics).
//     b. Set phase = Healing, patch status.
//     c. Build LLM client from spec, call Diagnose.
//     d. Store root cause + witty line in lastDiagnosis.
//     e. Emit K8s Event on the CR. Log gopher art + diagnosis.
//  5. On healthy: set phase = Healthy.
func (r *AegisWatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// --- 1. Fetch the AegisWatch CR ---
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

	// --- 2. Resolve target namespace ---
	targetNS := watch.Spec.TargetRef.Namespace
	if targetNS == "" {
		targetNS = watch.Namespace
	}

	// --- 3. Fetch the target Deployment ---
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

	// --- 4. Detect anomaly ---
	threshold := watch.Spec.RestartThreshold
	if threshold == 0 {
		threshold = 3
	}
	anomaly, reason, err := detectAnomaly(ctx, r.Client, &deployment, threshold)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("detecting anomaly: %w", err)
	}

	// --- 5. Handle anomaly or healthy ---
	statusPatch := client.MergeFrom(watch.DeepCopy())

	if !anomaly {
		watch.Status.Phase = opsv1alpha1.PhaseHealthy
		if err := r.Status().Patch(ctx, &watch, statusPatch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching status healthy: %w", err)
		}
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// --- Anomaly path ---
	logger.Info("🚨 Houston, we have a problem!",
		"deployment", deployKey,
		"reason", reason,
	)
	r.Recorder.Event(&watch, corev1.EventTypeWarning, "AnomalyDetected", reason)

	// Collect observability context.
	obs, err := r.Collector.Collect(ctx, &deployment, reason)
	if err != nil {
		logger.Error(err, "failed to collect observability context")
		// Continue with a minimal context so the LLM still gets something.
		obs = &observability.ObservabilityContext{
			DeploymentName: deployment.Name,
			Namespace:      deployment.Namespace,
			AnomalyReason:  reason,
			CollectedAt:    time.Now(),
		}
	} else {
		logger.Info(obs.Summary())
	}

	// Mark Healing before the LLM call (can be slow).
	now := metav1.Now()
	watch.Status.Phase = opsv1alpha1.PhaseHealing
	watch.Status.LastAnomalyTime = &now
	if err := r.Status().Patch(ctx, &watch, statusPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching status to Healing: %w", err)
	}

	// --- Call the LLM ---
	diagnosis, diagErr := r.runDiagnosis(ctx, &watch, obs)
	if diagErr != nil {
		logger.Error(diagErr, "LLM diagnosis failed — marking Degraded")
		r.Recorder.Event(&watch, corev1.EventTypeWarning, "DiagnosisFailed", diagErr.Error())

		// Re-fetch (status patch above changed resourceVersion).
		if err := r.Get(ctx, req.NamespacedName, &watch); err != nil {
			return ctrl.Result{}, fmt.Errorf("re-fetching AegisWatch after heal patch: %w", err)
		}
		patch2 := client.MergeFrom(watch.DeepCopy())
		watch.Status.Phase = opsv1alpha1.PhaseDegraded
		if err := r.Status().Patch(ctx, &watch, patch2); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching status to Degraded after LLM failure: %w", err)
		}
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// --- Store diagnosis in status ---
	diagSummary := fmt.Sprintf("%s\n\n💬 %s", diagnosis.RootCause, diagnosis.WittyLine)

	// Re-fetch before the final status patch.
	if err := r.Get(ctx, req.NamespacedName, &watch); err != nil {
		return ctrl.Result{}, fmt.Errorf("re-fetching AegisWatch before final patch: %w", err)
	}
	patch3 := client.MergeFrom(watch.DeepCopy())
	watch.Status.Phase = opsv1alpha1.PhaseDegraded // stays Degraded until PR is merged (Phase 4)
	watch.Status.LastDiagnosis = diagSummary
	if err := r.Status().Patch(ctx, &watch, patch3); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching final diagnosis status: %w", err)
	}

	// --- Emit K8s event and print gopher art ---
	r.Recorder.Event(&watch, corev1.EventTypeNormal, "DiagnosisComplete",
		fmt.Sprintf("AI diagnosis: %s", diagnosis.RootCause))

	logger.Info(gopherArt +
		"Root cause : " + diagnosis.RootCause + "\n" +
		"Witty line : " + diagnosis.WittyLine + "\n" +
		"YAML patch : " + yesNo(diagnosis.YAMLPatch != "") + "\n")

	// Phase 4 hook: open GitHub PR with diagnosis.YAMLPatch here.
	_ = diagnosis.YAMLPatch

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// runDiagnosis builds the LLM client from the CR spec and calls Diagnose.
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

// detectAnomaly scans pods belonging to the Deployment for known failure conditions.
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
				if w.Reason == "CrashLoopBackOff" || w.Reason == "OOMKilled" {
					return true, fmt.Sprintf(
						"pod %s/%s container %q is in %s",
						pod.Namespace, pod.Name, cs.Name, w.Reason,
					), nil
				}
			}
			if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
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
			targetNS := aw.Spec.TargetRef.Namespace
			if targetNS == "" {
				targetNS = aw.Namespace
			}
			if targetNS == pod.Namespace {
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
