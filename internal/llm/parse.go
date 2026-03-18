package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// stripMarkdownFences removes ```json ... ``` or ``` ... ``` wrappers that
// some LLMs add around JSON responses despite being asked for raw JSON.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			s = strings.TrimSuffix(s, "```")
			return strings.TrimSpace(s)
		}
	}
	return s
}

// parseDiagnosis decodes the LLM JSON content into a Diagnosis struct.
// It strips markdown code fences (```json ... ```) that some models add.
func parseDiagnosis(content string) (*Diagnosis, error) {
	content = stripMarkdownFences(content)
	var d Diagnosis
	if err := json.Unmarshal([]byte(content), &d); err != nil {
		return nil, fmt.Errorf("parsing LLM JSON response: %w\ncontent: %s", err, content)
	}
	if d.RootCause == "" {
		return nil, fmt.Errorf("LLM returned empty rootCause — likely a prompt/format issue")
	}
	return &d, nil
}
