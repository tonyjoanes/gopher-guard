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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	opsv1alpha1 "github.com/tonyjoanes/gopher-guard/api/v1alpha1"
)

const requeueInterval = 30 * time.Second

// AegisWatchReconciler reconciles an AegisWatch object.
type AegisWatchReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ops.gopherguard.dev,resources=aegiswatches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile is the main control loop. It:
//  1. Fetches the AegisWatch CR.
//  2. Looks up the target Deployment.
//  3. Scans pods for anomalies (CrashLoopBackOff, OOMKilled, excessive restarts).
//  4. Transitions .status.phase accordingly.
//
// Phase 2+ will add LLM diagnosis and GitHub PR creation.
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
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching Deployment %s: %w", deployKey, err)
	}

	// --- 4. Scan pods for anomalies ---
	anomaly, reason, err := r.detectAnomaly(ctx, &deployment, watch.Spec.RestartThreshold)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("detecting anomaly: %w", err)
	}

	// --- 5. Update status phase ---
	patch := client.MergeFrom(watch.DeepCopy())
	if anomaly {
		logger.Info("🚨 Houston, we have a problem!",
			"deployment", deployKey,
			"reason", reason,
		)
		watch.Status.Phase = opsv1alpha1.PhaseDegraded
		// Phase 3: call LLM here → open PR (Phase 4).
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
func (r *AegisWatchReconciler) detectAnomaly(
	ctx context.Context,
	deployment *appsv1.Deployment,
	restartThreshold int32,
) (bool, string, error) {
	selector, err := selectorFromDeployment(deployment)
	if err != nil {
		return false, "", err
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList,
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
func (r *AegisWatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.AegisWatch{}).
		Named("aegiswatch").
		Complete(r)
}
