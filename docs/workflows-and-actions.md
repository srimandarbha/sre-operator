# Workflows & Reusable Actions

The true power of the SRE Operator lies in its ability to execute **Directed Acyclic Graphs (DAGs)** for remediation. When an alert fires or a check fails, the operator can follow a multi-step runbook to diagnose and fix the issue.

## Defining a Workflow

Workflows are defined under the `remediationWorkflow` field of an alert trigger or check spec. A workflow is composed of multiple `steps`.

### Step Configuration

Each step supports the following fields:
* **`name`** (string): Unique ID for the step.
* **`action`** (string): The native Go function to execute (e.g., `DiagnoseLogs`, `RemediateDeleteResource`).
* **`dependsOn`** (list of strings): Which steps must finish before this step begins.
* **`runCondition`** (string): Evaluates the outcome of the `dependsOn` steps. Options are:
  * `OnSuccess` (Default): Runs only if all dependencies succeed.
  * `OnFailure`: Runs if ANY dependency fails. Used for fallback/emergency actions.
  * `Always`: Runs regardless of dependency outcomes.
* **`arguments`** (map of strings): Dynamic parameters passed to the action.

## The Reusable Actions Library

The SRE Operator ships with a built-in arsenal of diagnostic and remediation actions derived from standard OpenShift runbooks.

### Diagnostic Actions
These actions check state. If the state is unhealthy or a pattern is not found, the step **Fails**, which can trigger downstream `OnFailure` steps.

* **`DiagnoseLogs`**: Stream logs from a target pod.
  * `arguments`: `pattern` (string to search for), `lines` (number of tail lines).
* **`DiagnoseResourceStatus`**: Checks if a Pod, PVC, or Node is in a healthy/bound state.
* **`DiagnoseExecCommand`**: Executes a command inside a pod.
  * `arguments`: `command` (the shell command to run).
* **`DiagnoseNodeJournal`**: Spawns a debug pod to read `journalctl` on the host node.
* **`DiagnosePrometheusQuery`**: Evaluates a raw PromQL query.

### Remediation Actions
These actions make active changes to the cluster.

* **`RemediateDeleteResource`**: Deletes a pod/resource to force recreation.
* **`RemediatePatchResource`**: Applies a JSON patch (e.g., removing finalizers).
  * `arguments`: `patchType` (e.g., json, merge), `patchBody` (the raw patch string).
* **`RemediateScaleDeployment`**: Scales a Deployment up or down.
  * `arguments`: `replicas` (target count).
* **`RemediateVirtctlAction`**: Native equivalent of the `virtctl` CLI for KubeVirt VMs.
  * `arguments`: `command` (e.g., pause, stop, migrate).
* **`RemediateNodeSystemctl`**: Runs `systemctl restart` on an OpenShift host node.

## Example Runbook

Here is a full example of a workflow that tries to diagnose a stuck Virtual Machine, patches a finalizer if successful, or attempts to pause the VM if the diagnosis fails.

```yaml
alertTriggers:
  - name: "VirtualMachineStuck"
    alertName: "KubeVirtVMIExcessiveMigrations"
    remediationWorkflow:
      steps:
        - name: "check-launcher-logs"
          action: "DiagnoseLogs"
          arguments:
            pattern: "connection refused"
            lines: "100"
            
        - name: "remove-stuck-finalizer"
          action: "RemediatePatchResource"
          dependsOn: ["check-launcher-logs"]
          runCondition: "OnSuccess"
          arguments:
            kind: "VirtualMachineInstance"
            patchType: "json"
            patchBody: '[{"op": "remove", "path": "/metadata/finalizers"}]'

        - name: "pause-vm"
          action: "RemediateVirtctlAction"
          dependsOn: ["check-launcher-logs"]
          runCondition: "OnFailure"
          arguments:
            command: "pause"
```
