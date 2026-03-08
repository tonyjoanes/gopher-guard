// Package k8s provides shared Kubernetes client utilities.
package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadSecretKey fetches one string value from a Kubernetes Secret by key name.
// Returns an error if the secret or key is missing.
func ReadSecretKey(ctx context.Context, c client.Client, namespace, secretName, key string) (string, error) {
	if secretName == "" {
		return "", fmt.Errorf("secret name is empty")
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: secretName}, &secret); err != nil {
		return "", fmt.Errorf("get secret %s/%s: %w", namespace, secretName, err)
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, secretName, key)
	}
	return string(val), nil
}
