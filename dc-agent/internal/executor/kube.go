package executor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// kubevirtVMGVR is the GroupVersionResource for KubeVirt VirtualMachines. We
// list them through the dynamic client so the agent needn't depend on kubevirt's
// (heavy, version-touchy) client-go.
var kubevirtVMGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

// KubeExecutor reads zone inventory from the local Kubernetes (Harvester)
// cluster. The cluster credential lives here and never travels to dc-api.
type KubeExecutor struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface
}

// NewKubeExecutor builds a KubeExecutor from injected clients (used in tests).
func NewKubeExecutor(kube kubernetes.Interface, dyn dynamic.Interface) *KubeExecutor {
	return &KubeExecutor{kube: kube, dyn: dyn}
}

// NewKubeExecutorFromConfig builds a KubeExecutor against the local cluster. An
// empty kubeconfigPath uses in-cluster config — the production path, where the
// agent runs in the zone with a scoped ServiceAccount; a non-empty path loads
// that kubeconfig (local/dev, e.g. the agent on a laptop pointed at Harvester).
func NewKubeExecutorFromConfig(kubeconfigPath string) (*KubeExecutor, error) {
	var (
		cfg *rest.Config
		err error
	)
	if kubeconfigPath == "" {
		if cfg, err = rest.InClusterConfig(); err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	} else {
		if cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath); err != nil {
			return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfigPath, err)
		}
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return &KubeExecutor{kube: kube, dyn: dyn}, nil
}

// GetInventory lists nodes (readiness + allocatable/allocated CPU & memory) and
// counts KubeVirt VirtualMachines. "Allocated" is summed from pod resource
// requests, which needs no metrics-server.
func (k *KubeExecutor) GetInventory(ctx context.Context) (Inventory, error) {
	nodeList, err := k.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Inventory{}, fmt.Errorf("list nodes: %w", err)
	}

	usedCPU := map[string]int64{} // milli-cores requested, by node
	usedMem := map[string]int64{} // MiB requested, by node
	pods, err := k.kube.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Inventory{}, fmt.Errorf("list pods: %w", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue // unscheduled or terminal pods hold no live allocation
		}
		for j := range p.Spec.Containers {
			req := p.Spec.Containers[j].Resources.Requests
			usedCPU[p.Spec.NodeName] += req.Cpu().MilliValue()
			usedMem[p.Spec.NodeName] += req.Memory().Value() / (1024 * 1024)
		}
	}

	nodes := make([]Node, 0, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		alloc := n.Status.Allocatable
		nodes = append(nodes, Node{
			Name:             n.Name,
			Ready:            nodeReady(n),
			CPUAllocatableM:  alloc.Cpu().MilliValue(),
			CPUUsedM:         usedCPU[n.Name],
			MemAllocatableMB: alloc.Memory().Value() / (1024 * 1024),
			MemUsedMB:        usedMem[n.Name],
		})
	}

	vms, err := k.dyn.Resource(kubevirtVMGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Inventory{}, fmt.Errorf("list virtualmachines: %w", err)
	}

	return Inventory{Nodes: nodes, VMCount: len(vms.Items)}, nil
}

// nodeReady reports whether the node's Ready condition is True.
func nodeReady(n *corev1.Node) bool {
	for i := range n.Status.Conditions {
		if n.Status.Conditions[i].Type == corev1.NodeReady {
			return n.Status.Conditions[i].Status == corev1.ConditionTrue
		}
	}
	return false
}
