package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// LogTailLines is the number of log lines fetched per container.
	LogTailLines int64 = 50
)

// FetchContainerLogs retrieves the last LogTailLines lines from a container.
// It first tries the running container; if that fails (e.g. the container has
// crashed) it falls back to the previous terminated instance.
func FetchContainerLogs(
	ctx context.Context,
	kube kubernetes.Interface,
	namespace, podName, containerName string,
) (string, error) {
	tail := LogTailLines

	// Try current container logs first.
	logs, err := streamLogs(ctx, kube, namespace, podName, containerName, tail, false)
	if err == nil && logs != "" {
		return logs, nil
	}

	// Fall back to previous (terminated) container logs — the most useful
	// source when the container has just crashed.
	prev, prevErr := streamLogs(ctx, kube, namespace, podName, containerName, tail, true)
	if prevErr != nil {
		return "", fmt.Errorf("fetching logs for %s/%s[%s]: current: %v, previous: %v",
			namespace, podName, containerName, err, prevErr)
	}
	return "[previous container]\n" + prev, nil
}

func streamLogs(
	ctx context.Context,
	kube kubernetes.Interface,
	namespace, podName, containerName string,
	tailLines int64,
	previous bool,
) (string, error) {
	req := kube.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		TailLines: &tailLines,
		Previous:  previous,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", fmt.Errorf("reading log stream: %w", err)
	}
	return buf.String(), nil
}
