package executor

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubeExecutorGetInventory(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// A succeeded pod must NOT count toward allocation.
	done := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("8"),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	kube := fake.NewSimpleClientset(node, running, done)

	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
	vm.SetName("vm-1")
	vm.SetNamespace("default")
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vmGVR: "VirtualMachineList"},
		vm,
	)

	inv, err := NewKubeExecutor(kube, dyn).GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}

	if inv.VMCount != 1 {
		t.Errorf("VMCount = %d, want 1", inv.VMCount)
	}
	if len(inv.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(inv.Nodes))
	}
	n := inv.Nodes[0]
	if n.Name != "node-1" || !n.Ready {
		t.Errorf("node = %+v, want node-1 ready", n)
	}
	if n.CPUAllocatableM != 4000 {
		t.Errorf("CPUAllocatableM = %d, want 4000", n.CPUAllocatableM)
	}
	if n.CPUUsedM != 500 { // only the running pod; the succeeded pod's 8 cores excluded
		t.Errorf("CPUUsedM = %d, want 500", n.CPUUsedM)
	}
	if n.MemAllocatableMB != 8192 {
		t.Errorf("MemAllocatableMB = %d, want 8192", n.MemAllocatableMB)
	}
	if n.MemUsedMB != 1024 {
		t.Errorf("MemUsedMB = %d, want 1024", n.MemUsedMB)
	}
}

func TestNodeReady(t *testing.T) {
	notReady := &corev1.Node{Status: corev1.NodeStatus{
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}},
	}}
	if nodeReady(notReady) {
		t.Error("nodeReady = true for a NotReady node")
	}
	noCondition := &corev1.Node{}
	if nodeReady(noCondition) {
		t.Error("nodeReady = true for a node with no Ready condition")
	}
}
