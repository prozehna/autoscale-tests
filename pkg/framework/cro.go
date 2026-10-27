package framework

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

var clusterResourceOverrideGVR = schema.GroupVersionResource{
	Group:    "operator.autoscaling.openshift.io",
	Version:  "v1",
	Resource: "clusterresourceoverrides",
}

// resourceOverride is the namespaced CR, note that it lives in a different API
// group than the cluster scoped ClusterResourceOverride CR
var resourceOverrideGVR = schema.GroupVersionResource{
	Group:    "autoscaling.openshift.io",
	Version:  "v1",
	Resource: "resourceoverrides",
}

// CROConfig holds the override ratios for a ClusterResourceOverride CR
type CROConfig struct {
	LimitCPUToMemoryPercent     int64 // CPU limit = memory limit (in Mi) * percent / 100, expressed as millicores
	CPURequestToLimitPercent    int64 // CPU request = CPU limit * percent / 100
	MemoryRequestToLimitPercent int64 // Memory request = memory limit * percent / 100
}

// CreateClusterResourceOverride creates a ClusterResourceOverride CR
func (f *Framework) CreateClusterResourceOverride(ctx context.Context, cfg CROConfig) error {
	cro := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "operator.autoscaling.openshift.io/v1",
			"kind":       "ClusterResourceOverride",
			"metadata": map[string]interface{}{
				"name": "cluster",
			},
			"spec": map[string]interface{}{
				"podResourceOverride": map[string]interface{}{
					"spec": map[string]interface{}{
						"memoryRequestToLimitPercent": cfg.MemoryRequestToLimitPercent,
						"cpuRequestToLimitPercent":    cfg.CPURequestToLimitPercent,
						"limitCPUToMemoryPercent":     cfg.LimitCPUToMemoryPercent,
					},
				},
			},
		},
	}

	_, err := f.getDynamicClient().Resource(clusterResourceOverrideGVR).Create(ctx, cro, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			return f.UpdateClusterResourceOverride(ctx, cfg)
		}
		return fmt.Errorf("failed to create ClusterResourceOverride: %w", err)
	}
	fmt.Printf("[CRO] ClusterResourceOverride created\n")
	return nil
}

// UpdateClusterResourceOverride updates the existing ClusterResourceOverride CR with new ratios
func (f *Framework) UpdateClusterResourceOverride(ctx context.Context, cfg CROConfig) error {
	cro, err := f.getDynamicClient().Resource(clusterResourceOverrideGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get ClusterResourceOverride: %w", err)
	}

	_ = unstructured.SetNestedField(cro.Object, cfg.MemoryRequestToLimitPercent, "spec", "podResourceOverride", "spec", "memoryRequestToLimitPercent")
	_ = unstructured.SetNestedField(cro.Object, cfg.CPURequestToLimitPercent, "spec", "podResourceOverride", "spec", "cpuRequestToLimitPercent")
	_ = unstructured.SetNestedField(cro.Object, cfg.LimitCPUToMemoryPercent, "spec", "podResourceOverride", "spec", "limitCPUToMemoryPercent")

	_, err = f.getDynamicClient().Resource(clusterResourceOverrideGVR).Update(ctx, cro, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update ClusterResourceOverride: %w", err)
	}
	fmt.Printf("[CRO] ClusterResourceOverride updated\n")
	return nil
}

// DeleteClusterResourceOverride removes the ClusterResourceOverride CR
func (f *Framework) DeleteClusterResourceOverride(ctx context.Context) error {
	err := f.getDynamicClient().Resource(clusterResourceOverrideGVR).Delete(ctx, "cluster", metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// WaitForClusterResourceOverrideReady waits until the CRO admission webhook is available
func (f *Framework) WaitForClusterResourceOverrideReady(ctx context.Context, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		cro, err := f.getDynamicClient().Resource(clusterResourceOverrideGVR).Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		conditions, found, _ := unstructured.NestedSlice(cro.Object, "status", "conditions")
		if !found {
			return false, nil
		}
		for _, c := range conditions {
			cond, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cond["type"] == "Available" && cond["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

// ROConfig holds the override ratios for a namespaced ResourceOverride CR.
// PodSelector is optional, when it is empty the ResourceOverride matches every
// pod in the namespace.
type ROConfig struct {
	LimitCPUToMemoryPercent     int64
	CPURequestToLimitPercent    int64
	MemoryRequestToLimitPercent int64
	PodSelector                 map[string]string
}

// CreateResourceOverride creates a ResourceOverride CR in the given namespace
func (f *Framework) CreateResourceOverride(ctx context.Context, name, namespace string, cfg ROConfig) error {
	spec := map[string]interface{}{
		"podResourceOverride": map[string]interface{}{
			"memoryRequestToLimitPercent": cfg.MemoryRequestToLimitPercent,
			"cpuRequestToLimitPercent":    cfg.CPURequestToLimitPercent,
			"limitCPUToMemoryPercent":     cfg.LimitCPUToMemoryPercent,
		},
	}

	if len(cfg.PodSelector) > 0 {
		matchLabels := map[string]interface{}{}
		for k, v := range cfg.PodSelector {
			matchLabels[k] = v
		}
		spec["podSelector"] = map[string]interface{}{
			"matchLabels": matchLabels,
		}
	}

	ro := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "autoscaling.openshift.io/v1",
			"kind":       "ResourceOverride",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}

	_, err := f.getDynamicClient().Resource(resourceOverrideGVR).Namespace(namespace).Create(ctx, ro, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create ResourceOverride %s/%s: %w", namespace, name, err)
	}
	fmt.Printf("[CRO] ResourceOverride %s/%s created\n", namespace, name)
	return nil
}

// DeleteResourceOverride removes a ResourceOverride CR from the given namespace
func (f *Framework) DeleteResourceOverride(ctx context.Context, name, namespace string) error {
	err := f.getDynamicClient().Resource(resourceOverrideGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to delete ResourceOverride %s/%s: %w", namespace, name, err)
	}
	fmt.Printf("[CRO] ResourceOverride %s/%s deleted\n", namespace, name)
	return nil
}

// ResourceOverrideCRDExists tells if the ResourceOverride CRD is installed on the
// cluster, older CRO operator versions do not ship it
func (f *Framework) ResourceOverrideCRDExists(ctx context.Context) (bool, error) {
	return f.CRDExists(ctx, "autoscaling.openshift.io", "ResourceOverride")
}

// CreateCROOptInNamespace creates a namespace with the CRO opt-in label so the
// admission webhook will mutate pods created in it
func (f *Framework) CreateCROOptInNamespace(ctx context.Context, name string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"clusterresourceoverrides.admission.autoscaling.openshift.io/enabled": "true",
				"test-suite": "autoscale-tests",
			},
		},
	}
	err := f.Client.Create(ctx, ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create CRO opt-in namespace: %w", err)
	}
	return nil
}

// CreatePodWithResources creates a pod with the specified containers and waits for admission
func (f *Framework) CreatePodWithResources(ctx context.Context, name, namespace string, containers []corev1.Container) (*corev1.Pod, error) {
	return f.CreatePodWithResourcesAndInit(ctx, name, namespace, containers, nil)
}

// CreatePodWithResourcesAndInit creates a pod with containers and optional init containers
func (f *Framework) CreatePodWithResourcesAndInit(ctx context.Context, name, namespace string, containers, initContainers []corev1.Container) (*corev1.Pod, error) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			InitContainers: initContainers,
			Containers:     containers,
		},
	}

	if err := f.Client.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	created, err := f.GetPod(ctx, name, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get created pod: %w", err)
	}
	return created, nil
}

// CreateLimitRange creates a LimitRange in the specified namespace
func (f *Framework) CreateLimitRange(ctx context.Context, name, namespace string, spec corev1.LimitRangeSpec) error {
	lr := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}
	return f.Client.Create(ctx, lr)
}

// VerifyContainerResources checks that a container's actual resources match expected values
func VerifyContainerResources(container corev1.Container, expectedCPURequest, expectedMemRequest, expectedCPULimit, expectedMemLimit string) error {
	checks := []struct {
		name     string
		actual   resource.Quantity
		expected string
	}{
		{"cpu request", container.Resources.Requests[corev1.ResourceCPU], expectedCPURequest},
		{"memory request", container.Resources.Requests[corev1.ResourceMemory], expectedMemRequest},
		{"cpu limit", container.Resources.Limits[corev1.ResourceCPU], expectedCPULimit},
		{"memory limit", container.Resources.Limits[corev1.ResourceMemory], expectedMemLimit},
	}

	for _, c := range checks {
		if c.expected == "" {
			continue
		}
		expected := resource.MustParse(c.expected)
		if !c.actual.Equal(expected) {
			return fmt.Errorf("container %q %s: got %s, want %s",
				container.Name, c.name, c.actual.String(), expected.String())
		}
	}
	return nil
}

func (f *Framework) getDynamicClient() dynamic.Interface {
	return dynamic.NewForConfigOrDie(f.RestConfig)
}
