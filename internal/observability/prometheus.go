package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PrometheusClient queries a Prometheus-compatible HTTP API for workload metrics.
// Set URL to "" to disable Prometheus integration (Metrics will be nil).
type PrometheusClient struct {
	// URL is the base URL of the Prometheus server, e.g.
	// "http://prometheus-operated.monitoring.svc.cluster.local:9090"
	URL    string
	client *http.Client
}

// NewPrometheusClient creates a client with a sensible timeout.
func NewPrometheusClient(prometheusURL string) *PrometheusClient {
	return &PrometheusClient{
		URL: prometheusURL,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// QueryWorkload fetches CPU (millicores) and memory (MiB) for the pods
// matching the given deployment in the given namespace.
// Returns nil, nil when the Prometheus URL is empty (feature disabled).
func (p *PrometheusClient) QueryWorkload(
	ctx context.Context,
	namespace, deploymentName string,
) (*PrometheusSnapshot, error) {
	if p.URL == "" {
		return nil, nil
	}

	now := time.Now()

	// CPU: rate of container CPU seconds over 2m, summed across all containers
	// in pods whose labels contain the deployment name (heuristic — works for
	// standard Deployments created by kubebuilder/kubectl).
	cpuQuery := fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{namespace=%q, pod=~"%s-.*", container!=""}[2m])) * 1000`,
		namespace, deploymentName,
	)
	cpu, err := p.instantQuery(ctx, cpuQuery)
	if err != nil {
		return nil, fmt.Errorf("prometheus CPU query: %w", err)
	}

	// Memory: working set bytes → MiB.
	memQuery := fmt.Sprintf(
		`sum(container_memory_working_set_bytes{namespace=%q, pod=~"%s-.*", container!=""}) / 1024 / 1024`,
		namespace, deploymentName,
	)
	mem, err := p.instantQuery(ctx, memQuery)
	if err != nil {
		return nil, fmt.Errorf("prometheus memory query: %w", err)
	}

	return &PrometheusSnapshot{
		CPUUsageMillicores: cpu,
		MemUsageMiB:        mem,
		QueryTime:          now,
	}, nil
}

// prometheusResponse is the minimal shape of a Prometheus instant query response.
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value [2]json.RawMessage `json:"value"` // [timestamp, valueString]
		} `json:"result"`
	} `json:"data"`
}

func (p *PrometheusClient) instantQuery(ctx context.Context, query string) (float64, error) {
	endpoint := p.URL + "/api/v1/query"

	params := url.Values{}
	params.Set("query", query)
	params.Set("time", strconv.FormatInt(time.Now().Unix(), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("reading body: %w", err)
	}

	var pr prometheusResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return 0, fmt.Errorf("decoding response: %w", err)
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prometheus returned status %q", pr.Status)
	}
	if len(pr.Data.Result) == 0 {
		return 0, nil // no data — metric not yet available
	}

	// The second element of the value tuple is the numeric string.
	raw := pr.Data.Result[0].Value[1]
	var valStr string
	if err := json.Unmarshal(raw, &valStr); err != nil {
		return 0, fmt.Errorf("parsing value: %w", err)
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("converting value %q: %w", valStr, err)
	}
	return val, nil
}
