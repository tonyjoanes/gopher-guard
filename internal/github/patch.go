package github

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/yaml"
)

// ApplyYAMLPatch applies an LLM-generated strategic merge patch (YAML) to
// the current deployment YAML content and returns the patched YAML bytes.
//
// The patch must be a valid Kubernetes strategic merge patch fragment.
// If the patch string is empty the original content is returned unchanged.
func ApplyYAMLPatch(currentYAML []byte, patchYAML string) ([]byte, error) {
	if patchYAML == "" {
		return currentYAML, nil
	}

	// Convert both inputs to JSON (strategic merge patch operates on JSON).
	currentJSON, err := yaml.YAMLToJSON(currentYAML)
	if err != nil {
		return nil, fmt.Errorf("converting current YAML to JSON: %w", err)
	}

	patchJSON, err := yaml.YAMLToJSON([]byte(patchYAML))
	if err != nil {
		return nil, fmt.Errorf("converting patch YAML to JSON: %w", err)
	}

	// Apply strategic merge patch using Deployment as the schema type.
	patchedJSON, err := strategicpatch.StrategicMergePatch(currentJSON, patchJSON, appsv1.Deployment{})
	if err != nil {
		return nil, fmt.Errorf("applying strategic merge patch: %w", err)
	}

	// Convert back to YAML for the Git commit.
	patchedYAML, err := yaml.JSONToYAML(patchedJSON)
	if err != nil {
		return nil, fmt.Errorf("converting patched JSON back to YAML: %w", err)
	}

	return patchedYAML, nil
}
