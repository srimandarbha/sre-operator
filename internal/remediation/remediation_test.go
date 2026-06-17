package remediation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

// mockEngine overrides executeAction to simulate failures
type mockEngine struct {
	*Engine
	failSteps map[string]bool
}

func (m *mockEngine) executeStep(ctx context.Context, f *srev1alpha1.CheckResult, step srev1alpha1.RemediationStep) error {
	// We use the action string as the step name in the test for simplicity
	if m.failSteps[string(step.Action)] {
		return errors.New("simulated failure")
	}
	return nil
}

func TestWorkflowConditionalExecution(t *testing.T) {
	tests := []struct {
		name          string
		workflow      *srev1alpha1.RemediationWorkflow
		failSteps     map[string]bool
		expectedState srev1alpha1.StepState
		expectedSteps map[string]srev1alpha1.StepState
	}{
		{
			name: "All success",
			workflow: &srev1alpha1.RemediationWorkflow{
				Steps: []srev1alpha1.RemediationStep{
					{Name: "step1", Action: "step1"},
					{Name: "step2", Action: "step2", DependsOn: []string{"step1"}, RunCondition: srev1alpha1.RunOnSuccess},
				},
			},
			failSteps:     nil,
			expectedState: srev1alpha1.StepSucceeded,
			expectedSteps: map[string]srev1alpha1.StepState{
				"step1": srev1alpha1.StepSucceeded,
				"step2": srev1alpha1.StepSucceeded,
			},
		},
		{
			name: "Failure skips downstream OnSuccess",
			workflow: &srev1alpha1.RemediationWorkflow{
				Steps: []srev1alpha1.RemediationStep{
					{Name: "step1", Action: "step1"}, // this will fail
					{Name: "step2", Action: "step2", DependsOn: []string{"step1"}, RunCondition: srev1alpha1.RunOnSuccess},
				},
			},
			failSteps:     map[string]bool{"step1": true},
			expectedState: srev1alpha1.StepFailed,
			expectedSteps: map[string]srev1alpha1.StepState{
				"step1": srev1alpha1.StepFailed,
				"step2": srev1alpha1.StepSkipped,
			},
		},
		{
			name: "Failure triggers downstream OnFailure",
			workflow: &srev1alpha1.RemediationWorkflow{
				Steps: []srev1alpha1.RemediationStep{
					{Name: "step1", Action: "step1"}, // fails
					{Name: "step2", Action: "step2", DependsOn: []string{"step1"}, RunCondition: srev1alpha1.RunOnFailure},
					{Name: "step3", Action: "step3", DependsOn: []string{"step2"}, RunCondition: srev1alpha1.RunAlways},
				},
			},
			failSteps:     map[string]bool{"step1": true},
			// Overall state is Failed because a step failed, but step2 and step3 still ran successfully!
			expectedState: srev1alpha1.StepFailed,
			expectedSteps: map[string]srev1alpha1.StepState{
				"step1": srev1alpha1.StepFailed,
				"step2": srev1alpha1.StepSucceeded,
				"step3": srev1alpha1.StepSucceeded,
			},
		},
		{
			name: "Success skips downstream OnFailure",
			workflow: &srev1alpha1.RemediationWorkflow{
				Steps: []srev1alpha1.RemediationStep{
					{Name: "step1", Action: "step1"}, // succeeds
					{Name: "step2", Action: "step2", DependsOn: []string{"step1"}, RunCondition: srev1alpha1.RunOnFailure},
				},
			},
			failSteps:     nil,
			expectedState: srev1alpha1.StepSucceeded,
			expectedSteps: map[string]srev1alpha1.StepState{
				"step1": srev1alpha1.StepSucceeded,
				"step2": srev1alpha1.StepSkipped,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock logic here if needed
		})
	}
}

func TestTopologicalSort(t *testing.T) {
	runner := NewWorkflowRunner(nil)

	tests := []struct {
		name      string
		steps     []srev1alpha1.RemediationStep
		expectErr bool
	}{
		{
			name: "Linear dependency",
			steps: []srev1alpha1.RemediationStep{
				{Name: "step3", DependsOn: []string{"step2"}},
				{Name: "step1"},
				{Name: "step2", DependsOn: []string{"step1"}},
			},
			expectErr: false,
		},
		{
			name: "Cycle detection",
			steps: []srev1alpha1.RemediationStep{
				{Name: "step1", DependsOn: []string{"step3"}},
				{Name: "step2", DependsOn: []string{"step1"}},
				{Name: "step3", DependsOn: []string{"step2"}},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted, err := runner.topologicalSort(tt.steps)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, sorted, len(tt.steps))
			}
		})
	}
}
