package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SeverityLevel string

const (
	SeverityCritical SeverityLevel = "Critical"
	SeverityWarning  SeverityLevel = "Warning"
	SeverityInfo     SeverityLevel = "Info"
)

type CheckCategory string

const (
	CheckCategoryLogs       CheckCategory = "Logs"
	CheckCategoryOCPCluster CheckCategory = "OCPCluster"
	CheckCategoryPrometheus CheckCategory = "Prometheus"
	CheckCategoryVM         CheckCategory = "VM"
)

type RemediationAction string

const (
	RemediationNone     RemediationAction = "None"
	RemediationAlert    RemediationAction = "Alert"
	RemediationDiagnose RemediationAction = "Diagnose"
	RemediationRestart  RemediationAction = "Restart"
	RemediationMigrate  RemediationAction = "Migrate"
	RemediationDrain    RemediationAction = "Drain"
	RemediationScale    RemediationAction = "Scale"
	RemediationEvict    RemediationAction = "Evict"
	
	// New Reusable Library Actions
	ActionDiagnoseLogs           RemediationAction = "DiagnoseLogs"
	ActionDiagnoseResourceStatus RemediationAction = "DiagnoseResourceStatus"
	ActionDiagnoseExecCommand    RemediationAction = "DiagnoseExecCommand"
	ActionDiagnoseNodeJournal    RemediationAction = "DiagnoseNodeJournal"
	ActionDiagnosePrometheus     RemediationAction = "DiagnosePrometheusQuery"
	
	ActionRemediateDeleteResource  RemediationAction = "RemediateDeleteResource"
	ActionRemediatePatchResource   RemediationAction = "RemediatePatchResource"
	ActionRemediateScaleDeployment RemediationAction = "RemediateScaleDeployment"
	ActionRemediateVirtctlAction   RemediationAction = "RemediateVirtctlAction"
	ActionRemediateNodeSystemctl   RemediationAction = "RemediateNodeSystemctl"
)

type RunCondition string

const (
	RunOnSuccess RunCondition = "OnSuccess"
	RunOnFailure RunCondition = "OnFailure"
	RunAlways    RunCondition = "Always"
)

// RemediationStep defines a single action in a workflow DAG
type RemediationStep struct {
	// Name is the unique identifier for this step within the workflow
	Name string `json:"name"`
	// Action is the specific remediation to perform (e.g. Restart, CollectLogs)
	Action RemediationAction `json:"action"`
	// DependsOn specifies the names of steps that must succeed before this step runs
	DependsOn []string `json:"dependsOn,omitempty"`
	// Determines if this step runs based on the outcome of its dependencies
	RunCondition RunCondition `json:"runCondition,omitempty"`
	// Arguments passes dynamic parameters to the action (e.g. pattern="error")
	Arguments map[string]string `json:"arguments,omitempty"`
}

// RemediationWorkflow defines a DAG of steps to execute
type RemediationWorkflow struct {
	Steps []RemediationStep `json:"steps"`
}

type CheckResult struct {
	CheckName           string            `json:"checkName"`
	Category            CheckCategory     `json:"category"`
	ResourceRef         string            `json:"resourceRef"`
	Status              string            `json:"status"`
	Severity            SeverityLevel     `json:"severity"`
	Message             string            `json:"message"`
	LastChecked         *metav1.Time      `json:"lastChecked,omitempty"`
	TraceID             string            `json:"traceId,omitempty"`
	RemediationApplied  RemediationAction `json:"remediationApplied,omitempty"`
	RemediationAttempts int               `json:"remediationAttempts,omitempty"`
	RunbookURL          string            `json:"runbookUrl,omitempty"`
}

type CheckSpec struct {
	Name            string            `json:"name"`
	Category        CheckCategory     `json:"category"`
	Enabled         bool              `json:"enabled"`
	Severity        SeverityLevel     `json:"severity"`
	Remediation         RemediationAction    `json:"remediation,omitempty"`
	RemediationWorkflow *RemediationWorkflow `json:"remediationWorkflow,omitempty"`
	RunbookURL          string               `json:"runbookUrl,omitempty"`
	IntervalSeconds     int32                `json:"intervalSeconds,omitempty"`
}

type LogCollectionSpec struct {
	Enabled             bool     `json:"enabled"`
	CollectVMPodLogs    bool     `json:"collectVMPodLogs"`
	CollectOperatorLogs bool     `json:"collectOperatorLogs"`
	TailLines           int32    `json:"tailLines,omitempty"`
	SinceSeconds        int32    `json:"sinceSeconds,omitempty"`
	OnAlertOnly         bool     `json:"onAlertOnly,omitempty"`
	PersistToConfigMap  bool     `json:"persistToConfigMap,omitempty"`
	ErrorPatterns       []string `json:"errorPatterns,omitempty"`
}

type ObservabilitySpec struct {
	TracingEnabled bool   `json:"tracingEnabled,omitempty"`
	MetricsEnabled bool   `json:"metricsEnabled,omitempty"`
	LogsEnabled    bool   `json:"logsEnabled,omitempty"`
	OTelEndpoint   string `json:"otelEndpoint,omitempty"`
}

type OCPClusterSpec struct {
	Enabled                 bool              `json:"enabled"`
	CheckClusterOperators   bool              `json:"checkClusterOperators"`
	CheckClusterVersion     bool              `json:"checkClusterVersion"`
	CheckMachineConfigPools bool              `json:"checkMachineConfigPools"`
	Remediation             RemediationAction `json:"remediation,omitempty"`
	RunbookURL              string            `json:"runbookUrl,omitempty"`
}

type AlertTrigger struct {
	Name                     string            `json:"name"`
	AlertName                string            `json:"alertName"`
	Enabled                  bool              `json:"enabled"`
	Remediation              RemediationAction    `json:"remediation,omitempty"`
	RemediationWorkflow      *RemediationWorkflow `json:"remediationWorkflow,omitempty"`
	MaxFiringDurationMinutes int32                `json:"maxFiringDurationMinutes,omitempty"`
	CollectLogs              bool              `json:"collectLogs,omitempty"`
	CollectOCPClusterState   bool              `json:"collectOCPClusterState,omitempty"`
	RunbookURL               string            `json:"runbookUrl,omitempty"`
	LabelSelectors           map[string]string `json:"labelSelectors,omitempty"`
}

type MetricQuery struct {
	Name              string            `json:"name"`
	Expr              string            `json:"expr"`
	Description       string            `json:"description"`
	Operator          string            `json:"operator"`
	WarningThreshold  float64           `json:"warningThreshold"`
	CriticalThreshold float64           `json:"criticalThreshold"`
	Enabled           bool              `json:"enabled"`
	Remediation       RemediationAction `json:"remediation"`
	RunbookURL        string            `json:"runbookUrl,omitempty"`
}

type PrometheusSpec struct {
	Enabled       bool           `json:"enabled"`
	AlertTriggers []AlertTrigger `json:"alertTriggers,omitempty"`
	MetricQueries []MetricQuery  `json:"metricQueries,omitempty"`
}

type NotificationsSpec struct {
	EventsEnabled bool `json:"eventsEnabled"`
}

type SREPolicySpec struct {
	TargetNamespaces []string           `json:"targetNamespaces,omitempty"`
	Checks           []CheckSpec        `json:"checks,omitempty"`
	Paused           bool               `json:"paused,omitempty"`
	OCPCluster       *OCPClusterSpec    `json:"ocpCluster,omitempty"`
	Prometheus       *PrometheusSpec    `json:"prometheus,omitempty"`
	LogCollection    *LogCollectionSpec `json:"logCollection,omitempty"`
	Notifications    *NotificationsSpec `json:"notifications,omitempty"`
}

type StepState string

const (
	StepPending   StepState = "Pending"
	StepRunning   StepState = "Running"
	StepSucceeded StepState = "Succeeded"
	StepFailed    StepState = "Failed"
	StepSkipped   StepState = "Skipped"
)

type WorkflowStepStatus struct {
	Name       string       `json:"name"`
	State      StepState    `json:"state"`
	Message    string       `json:"message,omitempty"`
	StartTime  *metav1.Time `json:"startTime,omitempty"`
	FinishTime *metav1.Time `json:"finishTime,omitempty"`
}

type WorkflowExecutionStatus struct {
	ID             string               `json:"id"`
	WorkflowName   string               `json:"workflowName"`
	TargetResource string               `json:"targetResource"`
	State          StepState            `json:"state"`
	Steps          []WorkflowStepStatus `json:"steps,omitempty"`
	StartTime      *metav1.Time         `json:"startTime,omitempty"`
}

type SREPolicyStatus struct {
	Findings                []CheckResult             `json:"findings,omitempty"`
	ActiveWorkflows         []WorkflowExecutionStatus `json:"activeWorkflows,omitempty"`
	LastScanTime            *metav1.Time       `json:"lastScanTime,omitempty"`
	NextScanTime            *metav1.Time       `json:"nextScanTime,omitempty"`
	TotalChecks             int32              `json:"totalChecks,omitempty"`
	RemediationsApplied     int32              `json:"remediationsApplied,omitempty"`
	ObservedGeneration      int64              `json:"observedGeneration,omitempty"`
	HealthyCount            int32              `json:"healthyCount,omitempty"`
	DegradedCount           int32              `json:"degradedCount,omitempty"`
	FailedCount             int32              `json:"failedCount,omitempty"`
	Phase                   string             `json:"phase,omitempty"`
	Conditions              []metav1.Condition `json:"conditions,omitempty"`
	OCPClusterHealthy       *bool              `json:"ocpClusterHealthy,omitempty"`
	FiringAlertCount        int32              `json:"firingAlertCount,omitempty"`
	FiringAlertNames               []string           `json:"firingAlertNames,omitempty"`
	ObservabilityEndpointReachable *bool              `json:"observabilityEndpointReachable,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type SREPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SREPolicySpec   `json:"spec,omitempty"`
	Status SREPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type SREPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SREPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SREPolicy{}, &SREPolicyList{})
}
