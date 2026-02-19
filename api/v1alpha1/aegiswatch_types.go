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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LLMProvider enumerates the supported LLM backends.
// +kubebuilder:validation:Enum=groq;ollama;openai
type LLMProvider string

const (
	LLMProviderGroq   LLMProvider = "groq"
	LLMProviderOllama LLMProvider = "ollama"
	LLMProviderOpenAI LLMProvider = "openai"
)

// AegisWatchPhase represents the current state of healing activity.
// +kubebuilder:validation:Enum=Watching;Degraded;Healing;Healthy
type AegisWatchPhase string

const (
	PhaseWatching AegisWatchPhase = "Watching"
	PhaseDegraded AegisWatchPhase = "Degraded"
	PhaseHealing  AegisWatchPhase = "Healing"
	PhaseHealthy  AegisWatchPhase = "Healthy"
)

// TargetRef identifies the Deployment that AegisWatch monitors.
type TargetRef struct {
	// Name of the Deployment to watch.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the Deployment. Defaults to the AegisWatch namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// AegisWatchSpec defines the desired state of AegisWatch.
type AegisWatchSpec struct {
	// TargetRef points to the Deployment that GopherGuard should watch and heal.
	// +kubebuilder:validation:Required
	TargetRef TargetRef `json:"targetRef"`

	// LLMProvider selects which LLM backend to use for diagnosis.
	// +kubebuilder:default=groq
	LLMProvider LLMProvider `json:"llmProvider,omitempty"`

	// LLMModel is the model identifier sent to the LLM provider.
	// e.g. "llama3-70b-8192" for Groq, "llama3" for Ollama.
	// +kubebuilder:default="llama3-70b-8192"
	LLMModel string `json:"llmModel,omitempty"`

	// LLMSecretRef names a Kubernetes Secret that contains the LLM API key
	// under the key "apiKey". Not required for Ollama.
	// +optional
	LLMSecretRef string `json:"llmSecretRef,omitempty"`

	// GitRepo is the "owner/repo" string for the GitHub repository where
	// GopherGuard will open pull requests with YAML patches.
	// +kubebuilder:validation:Required
	GitRepo string `json:"gitRepo"`

	// GitSecretRef names a Kubernetes Secret containing a GitHub token
	// under the key "token".
	// +kubebuilder:validation:Required
	GitSecretRef string `json:"gitSecretRef"`

	// SafeMode disables automatic PR creation; GopherGuard will only log
	// the diagnosis and suggested patch.
	// +kubebuilder:default=false
	SafeMode bool `json:"safeMode,omitempty"`

	// RestartThreshold is the number of container restarts that triggers
	// an anomaly. Defaults to 3.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	RestartThreshold int32 `json:"restartThreshold,omitempty"`
}

// AegisWatchStatus defines the observed state of AegisWatch.
type AegisWatchStatus struct {
	// Phase is the current healing lifecycle phase.
	// +optional
	Phase AegisWatchPhase `json:"phase,omitempty"`

	// LastDiagnosis holds the most recent LLM root-cause summary and witty line.
	// +optional
	LastDiagnosis string `json:"lastDiagnosis,omitempty"`

	// LastPRURL is the GitHub Pull Request URL created by the last healing cycle.
	// +optional
	LastPRURL string `json:"lastPRUrl,omitempty"`

	// HealingScore counts the number of successful healing PRs created.
	// +optional
	HealingScore int32 `json:"healingScore,omitempty"`

	// LastAnomalyTime records when the most recent anomaly was detected.
	// +optional
	LastAnomalyTime *metav1.Time `json:"lastAnomalyTime,omitempty"`

	// Conditions provides standard Kubernetes condition reporting.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".spec.targetRef.name"
// +kubebuilder:printcolumn:name="HealingScore",type=integer,JSONPath=".status.healingScore"
// +kubebuilder:printcolumn:name="SafeMode",type=boolean,JSONPath=".spec.safeMode"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// AegisWatch is the Schema for the aegiswatches API.
type AegisWatch struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AegisWatchSpec   `json:"spec,omitempty"`
	Status AegisWatchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AegisWatchList contains a list of AegisWatch.
type AegisWatchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AegisWatch `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AegisWatch{}, &AegisWatchList{})
}
