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
	"github.com/tonyjoanes/gopher-guard/internal/observability"
)

const requeueInterval = 30 * time.Second

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
//  4. On anomaly: collect full ObservabilityContext (logs + events + metrics),
//     emit a Kubernetes Event on the CR, update .status.phase = Degraded,
//     record lastAnomalyTime.
//  5. On healthy: update .status.phase = Healthy.
//
// Phase 3 will call the LLM with the ObservabilityContext.
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
		threshold = 3 // spec default may not be applied yet on brand-new objects
	}
	anomaly, reason, err := detectAnomaly(ctx, r.Client, &deployment, threshold)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("detecting anomaly: %w", err)
	}

	// --- 5. Collect observability data and update status ---
	patch := client.MergeFrom(watch.DeepCopy())

	if anomaly {
		logger.Info("🚨 Houston, we have a problem!",
			"deployment", deployKey,
			"reason", reason,
		)

		// Emit a Kubernetes Warning event on the AegisWatch CR.
		r.Recorder.Event(&watch, corev1.EventTypeWarning, "AnomalyDetected", reason)

		// Collect full observability context (logs + events + metrics).
		obs, err := r.Collector.Collect(ctx, &deployment, reason)
		if err != nil {
			// Non-fatal: log and continue — we still update the phase.
			logger.Error(err, "failed to collect observability context")
		} else {
			logger.Info(obs.Summary())
			// Phase 3 hook: pass obs to LLM here.
			_ = obs
		}

		now := metav1.Now()
		watch.Status.Phase = opsv1alpha1.PhaseDegraded
		watch.Status.LastAnomalyTime = &now

	} else {
		watch.Status.Phase = opsv1alpha1.PhaseHealthy
	}

	if err := r.Status().Patch(ctx, &watch, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching AegisWatch status: %w", err)
	}

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// detectAnomaly scans pods belonging to the Deployment for known failure conditions.
// Returns (true, reason, nil) when an anomaly is found.
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

// selectorFromDeployment extracts pod matchLabels from a Deployment spec.
func selectorFromDeployment(d *appsv1.Deployment) (map[string]string, error) {
	if d.Spec.Selector == nil || len(d.Spec.Selector.MatchLabels) == 0 {
		return nil, fmt.Errorf("deployment %s/%s has no matchLabels selector", d.Namespace, d.Name)
	}
	return d.Spec.Selector.MatchLabels, nil
}

// SetupWithManager wires the reconciler into the controller-runtime manager.
// It watches:
//   - AegisWatch CRs (primary)
//   - Pods: re-enqueues every AegisWatch whose target namespace matches the pod's
//     namespace, so the controller reacts immediately to pod restarts/crashes.
func (r *AegisWatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// podToAegisWatches maps a Pod event to the AegisWatch CRs that monitor it.
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
		// Re-reconcile when any Pod in a watched namespace changes state.
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(podToAegisWatches),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("aegiswatch").
		Complete(r)
}
