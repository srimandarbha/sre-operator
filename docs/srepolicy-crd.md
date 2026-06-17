# SREPolicy CRD Reference

The `SREPolicy` Custom Resource is the primary configuration object for the SRE Operator. You can deploy one or multiple policies targeting different namespaces or environments.

## High-Level Structure

A typical `SREPolicy` looks like this:

```yaml
apiVersion: sre.kubevirt.io/v1alpha1
kind: SREPolicy
metadata:
  name: prod-vm-policy
  namespace: production
spec:
  targetNamespaces:
    - "production"
    - "staging"
  
  # Configuration for built-in Go checks (VM health, Node health)
  checks:
    - name: "vm-health-check"
      category: "VM"
      enabled: true
      intervalSeconds: 60
      
  # Integration with Prometheus / AlertManager
  prometheus:
    enabled: true
    alertTriggers:
      - name: "NodeMemoryPressure"
        alertName: "NodeHighMemory"
        enabled: true
        # See Workflows documentation for how to configure this
        remediationWorkflow: 
          steps: []
```

## Status Tracking

The SRE Operator provides a highly detailed `status` subresource. It reports:
1. `findings`: A list of all healthy, degraded, or failed resources discovered during checks.
2. `activeWorkflows`: Real-time tracking of any currently executing DAG remediations, showing the exact state (`Pending`, `Running`, `Succeeded`, `Failed`, `Skipped`) of each step.

```yaml
status:
  phase: "Active"
  healthyCount: 15
  degradedCount: 2
  activeWorkflows:
    - id: "pod-123-OOMKilled"
      workflowName: "PodRestartWorkflow"
      targetResource: "pod/database-primary"
      state: "Running"
      steps:
        - name: "check-logs"
          state: "Succeeded"
        - name: "restart-pod"
          state: "Running"
```
