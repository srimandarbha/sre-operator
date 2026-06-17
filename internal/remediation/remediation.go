package remediation

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
	"github.com/openshift-virtualization/sre-operator/internal/prometheus"
)

type AlertDispatchResult struct {
	Error          error
	Skipped        bool
	Action         srev1alpha1.RemediationAction
	TriggerName    string
	TargetResource string
}

type Engine struct {
	client client.Client
	tracer trace.Tracer
}

func NewEngine(c client.Client, t trace.Tracer) *Engine {
	return &Engine{client: c, tracer: t}
}

// Apply executes a single action. DAGs are managed by the WorkflowRunner directly.
func (e *Engine) Apply(ctx context.Context, f *srev1alpha1.CheckResult, spec srev1alpha1.CheckSpec) (srev1alpha1.RemediationAction, error) {
	if spec.RemediationWorkflow != nil && len(spec.RemediationWorkflow.Steps) > 0 {
		return srev1alpha1.RemediationNone, fmt.Errorf("use WorkflowRunner for DAGs")
	}
	// Fallback for legacy single action
	dummyStep := srev1alpha1.RemediationStep{
		Action: spec.Remediation,
	}
	err := e.executeStep(ctx, f, dummyStep)
	return spec.Remediation, err
}

func (e *Engine) executeStep(ctx context.Context, f *srev1alpha1.CheckResult, step srev1alpha1.RemediationStep) error {
	switch step.Action {
	case srev1alpha1.ActionDiagnoseLogs:
		pattern := step.Arguments["pattern"]
		if pattern == "" {
			return fmt.Errorf("missing 'pattern' argument for DiagnoseLogs")
		}
		// TODO: Implement actual pod log streaming and pattern matching
		return nil

	case srev1alpha1.ActionRemediatePatchResource:
		patchType := step.Arguments["patchType"]
		patchBody := step.Arguments["patchBody"]
		if patchType == "" || patchBody == "" {
			return fmt.Errorf("missing 'patchType' or 'patchBody' argument")
		}
		// TODO: Implement actual client.Patch
		return nil

	case srev1alpha1.ActionRemediateVirtctlAction:
		command := step.Arguments["command"]
		if command == "" {
			return fmt.Errorf("missing 'command' argument")
		}
		// TODO: Implement actual virtctl equivalent execution
		return nil
		
	// Add other new reusable actions here...
	
	default:
		// Fallback for generic actions (Restart, Evict, etc.)
		return nil
	}
}

type AlertDispatcher struct {
	engine *Engine
	tracer trace.Tracer
}

func NewAlertDispatcher(e *Engine, t trace.Tracer) *AlertDispatcher {
	return &AlertDispatcher{engine: e, tracer: t}
}

func (d *AlertDispatcher) Dispatch(ctx context.Context, results []prometheus.AlertTriggerResult, triggers []srev1alpha1.AlertTrigger, findings []srev1alpha1.CheckResult) []AlertDispatchResult {
	// TODO: Stub for AlertDispatcher
	return nil
}

// ── Workflow Runner (DAG) ─────────────────────────────────────────────────────

type WorkflowRunner struct {
	engine *Engine
}

func NewWorkflowRunner(e *Engine) *WorkflowRunner {
	return &WorkflowRunner{engine: e}
}

// Run executes a single iteration of the workflow, returning the updated state.
// It is designed to be called by the reconciler loop.
func (r *WorkflowRunner) Run(ctx context.Context, f *srev1alpha1.CheckResult, workflow *srev1alpha1.RemediationWorkflow, state *srev1alpha1.WorkflowExecutionStatus) error {
	sortedSteps, err := r.topologicalSort(workflow.Steps)
	if err != nil {
		state.State = srev1alpha1.StepFailed
		return fmt.Errorf("DAG validation failed: %w", err)
	}

	if state.State == "" {
		state.State = srev1alpha1.StepRunning
		now := metav1.Now()
		state.StartTime = &now
	}

	if state.State == srev1alpha1.StepSucceeded || state.State == srev1alpha1.StepFailed {
		return nil // Already terminal
	}

	// Initialize step states if empty
	if len(state.Steps) == 0 {
		for _, step := range sortedSteps {
			state.Steps = append(state.Steps, srev1alpha1.WorkflowStepStatus{
				Name:  step.Name,
				State: srev1alpha1.StepPending,
			})
		}
	}

	// Helper to lookup step state
	getStepState := func(name string) srev1alpha1.StepState {
		for _, s := range state.Steps {
			if s.Name == name {
				return s.State
			}
		}
		return srev1alpha1.StepPending
	}
	
	setStepStatus := func(name string, stepState srev1alpha1.StepState, msg string) {
		for i, s := range state.Steps {
			if s.Name == name {
				state.Steps[i].State = stepState
				state.Steps[i].Message = msg
				if stepState != srev1alpha1.StepPending && stepState != srev1alpha1.StepRunning && s.FinishTime == nil {
					now := metav1.Now()
					state.Steps[i].FinishTime = &now
				}
				return
			}
		}
	}

	allComplete := true
	anyFailed := false

	for _, step := range sortedSteps {
		currentStatus := getStepState(step.Name)
		if currentStatus == srev1alpha1.StepSucceeded || currentStatus == srev1alpha1.StepSkipped || currentStatus == srev1alpha1.StepFailed {
			if currentStatus == srev1alpha1.StepFailed {
				anyFailed = true
			}
			continue
		}
		
		allComplete = false

		// Check dependencies
		depsSatisfied := true
		anyDepFailed := false
		for _, dep := range step.DependsOn {
			ds := getStepState(dep)
			if ds == srev1alpha1.StepPending || ds == srev1alpha1.StepRunning {
				depsSatisfied = false
				break
			}
			if ds == srev1alpha1.StepFailed || ds == srev1alpha1.StepSkipped {
				anyDepFailed = true
			}
		}

		if !depsSatisfied {
			continue // Wait for dependencies
		}

		// Evaluate conditional branch
		runCondition := step.RunCondition
		if runCondition == "" {
			runCondition = srev1alpha1.RunOnSuccess
		}

		shouldRun := false
		switch runCondition {
		case srev1alpha1.RunAlways:
			shouldRun = true
		case srev1alpha1.RunOnFailure:
			if anyDepFailed {
				shouldRun = true
			} else if len(step.DependsOn) > 0 {
				setStepStatus(step.Name, srev1alpha1.StepSkipped, "Skipped (dependencies succeeded)")
				continue
			} else {
				shouldRun = true // No deps, run anyway
			}
		case srev1alpha1.RunOnSuccess:
			if anyDepFailed {
				setStepStatus(step.Name, srev1alpha1.StepSkipped, "Skipped (dependency failed)")
				continue
			} else {
				shouldRun = true
			}
		}

		if shouldRun {
			// Execute
			setStepStatus(step.Name, srev1alpha1.StepRunning, "")
			actionErr := r.engine.executeStep(ctx, f, step)
			
			// Normally execution might be asynchronous, but for simplicity here we assume synchronous.
			// In a real operator, this might check external state.
			if actionErr != nil {
				setStepStatus(step.Name, srev1alpha1.StepFailed, actionErr.Error())
				anyFailed = true
			} else {
				// Simulating synchronous success for stub
				// In a real scenario, we might return here and wait for next reconcile if it's long-running.
				time.Sleep(10 * time.Millisecond) 
				setStepStatus(step.Name, srev1alpha1.StepSucceeded, "Action completed")
			}
		}
	}

	if allComplete {
		if anyFailed {
			state.State = srev1alpha1.StepFailed
		} else {
			state.State = srev1alpha1.StepSucceeded
		}
	}

	return nil
}

func (r *WorkflowRunner) topologicalSort(steps []srev1alpha1.RemediationStep) ([]srev1alpha1.RemediationStep, error) {
	graph := make(map[string][]string)
	inDegree := make(map[string]int)
	stepMap := make(map[string]srev1alpha1.RemediationStep)

	for _, step := range steps {
		stepMap[step.Name] = step
		if _, ok := inDegree[step.Name]; !ok {
			inDegree[step.Name] = 0
		}
		for _, dep := range step.DependsOn {
			graph[dep] = append(graph[dep], step.Name)
			inDegree[step.Name]++
		}
	}

	var queue []string
	for name, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, name)
		}
	}

	var sorted []srev1alpha1.RemediationStep
	count := 0

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		step, exists := stepMap[curr]
		if !exists {
			return nil, fmt.Errorf("dependency %q not found in steps", curr)
		}
		sorted = append(sorted, step)
		count++

		for _, neighbor := range graph[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if count != len(steps) {
		return nil, fmt.Errorf("cycle detected in remediation workflow")
	}

	return sorted, nil
}
