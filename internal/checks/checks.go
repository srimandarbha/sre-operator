package checks

import (
	"context"

	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/client"

	srev1alpha1 "github.com/openshift-virtualization/sre-operator/api/v1alpha1"
)

type Checker interface {
	Name() string
	Run(ctx context.Context, spec srev1alpha1.CheckSpec) ([]srev1alpha1.CheckResult, error)
}

type Registry struct{}

func NewRegistry() *Registry { return &Registry{} }
func (r *Registry) Register(c Checker) {}
func (r *Registry) For(category srev1alpha1.CheckCategory) []Checker { return nil }

type dummyChecker struct{ name string }

func (d *dummyChecker) Name() string { return d.name }
func (d *dummyChecker) Run(ctx context.Context, spec srev1alpha1.CheckSpec) ([]srev1alpha1.CheckResult, error) {
	return nil, nil
}

func NewVMHealthChecker(c client.Client, t trace.Tracer, ns string) Checker { return &dummyChecker{"vm"} }
func NewNodeHealthChecker(c client.Client, t trace.Tracer) Checker { return &dummyChecker{"node"} }
func NewStorageChecker(c client.Client, t trace.Tracer, ns string) Checker { return &dummyChecker{"storage"} }
func NewNetworkChecker(c client.Client, t trace.Tracer, ns string) Checker { return &dummyChecker{"network"} }
func NewOCPClusterChecker(c client.Client, t trace.Tracer) Checker { return &dummyChecker{"ocp"} }
