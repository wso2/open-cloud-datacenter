package executor

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// defaultFieldManager owns the fields a server-side apply sets when the
	// caller leaves field_manager empty. dc-api is the field owner end to end.
	defaultFieldManager = "dc-api"

	// defaultWatchSnapshots / maxWatchSnapshots bound a watch_status stream when
	// the caller omits / over-asks max_snapshots.
	defaultWatchSnapshots = 10
	maxWatchSnapshots     = 100
)

// kubevirtVMGVR is the GroupVersionResource for KubeVirt VirtualMachines. We
// list them through the dynamic client so the agent needn't depend on kubevirt's
// (heavy, version-touchy) client-go.
var kubevirtVMGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

// KubeExecutor reads zone inventory from, and mutates, the local Kubernetes
// (Harvester) cluster. The cluster credential lives here and never travels to
// dc-api. mapper resolves GVK→GVR for the mutating/status verbs (M-B) and may be
// nil for an inventory-only executor.
type KubeExecutor struct {
	kube   kubernetes.Interface
	dyn    dynamic.Interface
	mapper meta.RESTMapper
}

// NewKubeExecutor builds a KubeExecutor from injected clients (used in tests).
// The RESTMapper is nil — get_inventory needs no mapping. Tests exercising the
// M-B verbs use NewKubeExecutorWithMapper with a static mapper.
func NewKubeExecutor(kube kubernetes.Interface, dyn dynamic.Interface) *KubeExecutor {
	return &KubeExecutor{kube: kube, dyn: dyn}
}

// NewKubeExecutorWithMapper builds a KubeExecutor with an explicit RESTMapper —
// the test-injection path for the GVK→GVR-resolving M-B verbs, where a static
// meta.RESTMapper stands in for cluster discovery.
func NewKubeExecutorWithMapper(kube kubernetes.Interface, dyn dynamic.Interface, mapper meta.RESTMapper) *KubeExecutor {
	return &KubeExecutor{kube: kube, dyn: dyn, mapper: mapper}
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
	// A deferred, memcached discovery RESTMapper resolves GVK→GVR for the M-B
	// verbs. Deferred + memcache means it lazy-loads discovery on first use and
	// can Reset() on a NoMatch (e.g. a freshly-installed CRD), so a manifest for
	// a kind that appeared after startup still resolves.
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco))
	return &KubeExecutor{kube: kube, dyn: dyn, mapper: mapper}, nil
}

// resolve maps a ResourceRef's GVK to the dynamic ResourceInterface scoped to
// its namespace (or cluster-wide). A GVK NoMatch — an unknown/uninstalled kind —
// is a BAD_REQUEST-class fault; a missing name is too. On a NoMatch the deferred
// mapper is Reset so a subsequent call re-reads discovery (handles a CRD that was
// installed after the mapper last cached).
func (k *KubeExecutor) resolve(ref ResourceRef) (dynamic.ResourceInterface, error) {
	if k.mapper == nil {
		return nil, badRequest(fmt.Errorf("no RESTMapper configured (inventory-only executor)"))
	}
	if ref.Name == "" {
		return nil, badRequest(fmt.Errorf("name is required"))
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil, badRequest(fmt.Errorf("invalid api_version %q: %w", ref.APIVersion, err))
	}
	gvk := gv.WithKind(ref.Kind)
	mapping, err := k.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		if meta.IsNoMatchError(err) {
			if reset, ok := k.mapper.(interface{ Reset() }); ok {
				reset.Reset()
			}
			return nil, badRequest(fmt.Errorf("unknown kind %s: %w", gvk, err))
		}
		return nil, fmt.Errorf("rest mapping for %s: %w", gvk, err)
	}
	client := k.dyn.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		return client.Namespace(ref.Namespace), nil
	}
	return client, nil // cluster-scoped: namespace is ignored
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

// Apply server-side-applies manifest to the agent's own cluster. The manifest's
// GVK/namespace/name are taken from the object itself — the agent never injects
// a namespace and never cross-cluster-routes (the manifest's cluster is always
// this zone's, token-authoritative). force takes ownership of fields another
// manager holds; without it a conflict surfaces as an EXEC_ERROR.
//
// TODO(rbac): apply needs create+patch on the managed CR groups (kubevirt,
// KubeOVN, Rancher provisioning, …). The agent's current in-cluster ClusterRole
// is read-only; against a live cluster this returns Forbidden until the widened
// role lands (separate Flux/manifest follow-up PR — not this change). M-B tests
// use fake clients with no RBAC, so they are unaffected.
func (k *KubeExecutor) Apply(ctx context.Context, manifest json.RawMessage, fieldManager string, force bool) (ApplyResult, error) {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(manifest); err != nil {
		return ApplyResult{}, badRequest(fmt.Errorf("unparseable manifest: %w", err))
	}
	gvk := obj.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return ApplyResult{}, badRequest(fmt.Errorf("manifest missing apiVersion/kind"))
	}
	name := obj.GetName()
	if name == "" {
		return ApplyResult{}, badRequest(fmt.Errorf("manifest missing metadata.name"))
	}
	if fieldManager == "" {
		fieldManager = defaultFieldManager
	}

	// Server-side apply rejects an incoming object that already carries
	// metadata.managedFields ("cannot apply an object with managed fields already
	// set"), so strip it. Also clear metadata.resourceVersion: an apply declares
	// desired state and must not assert an optimistic-concurrency precondition the
	// caller didn't intend (a stale RV would spuriously conflict). Both are
	// server-managed and safe to drop from a manifest we re-apply.
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")

	ri, err := k.resolve(ResourceRef{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Namespace:  obj.GetNamespace(),
		Name:       name,
	})
	if err != nil {
		return ApplyResult{}, err
	}

	applied, err := ri.Apply(ctx, name, obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: force})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply %s %s/%s: %w", gvk, obj.GetNamespace(), name, err)
	}
	return ApplyResult{
		APIVersion:      gvk.GroupVersion().String(),
		Kind:            gvk.Kind,
		Namespace:       applied.GetNamespace(),
		Name:            applied.GetName(),
		UID:             string(applied.GetUID()),
		ResourceVersion: applied.GetResourceVersion(),
	}, nil
}

// Delete removes the referenced object. A missing object is success
// (Existed=false) — the idempotent-delete contract. An illegal propagation
// policy is a BAD_REQUEST.
//
// TODO(rbac): delete needs the delete verb on the managed CR groups; see the
// note on Apply. Live calls return Forbidden until the widened ClusterRole lands.
func (k *KubeExecutor) Delete(ctx context.Context, ref ResourceRef, propagationPolicy string) (DeleteResult, error) {
	opts := metav1.DeleteOptions{}
	if propagationPolicy != "" {
		policy, err := parsePropagationPolicy(propagationPolicy)
		if err != nil {
			return DeleteResult{}, err
		}
		opts.PropagationPolicy = &policy
	}

	ri, err := k.resolve(ref)
	if err != nil {
		return DeleteResult{}, err
	}

	if err := ri.Delete(ctx, ref.Name, opts); err != nil {
		if apierrors.IsNotFound(err) {
			return DeleteResult{Existed: false}, nil
		}
		return DeleteResult{}, fmt.Errorf("delete %s/%s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
	}
	return DeleteResult{Existed: true}, nil
}

// GetStatus reads the referenced object's status once. A missing object is
// success (Found=false).
func (k *KubeExecutor) GetStatus(ctx context.Context, ref ResourceRef) (StatusSnapshot, error) {
	ri, err := k.resolve(ref)
	if err != nil {
		return StatusSnapshot{}, err
	}
	obj, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return StatusSnapshot{Found: false}, nil
		}
		return StatusSnapshot{}, fmt.Errorf("get %s/%s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
	}
	return snapshotOf(obj)
}

// WatchStatus opens a watch on the single named object and calls emit once per
// observed Added/Modified event, stopping after maxSnapshots events, on
// ctx.Done(), or when the watch channel closes/errors. It never touches the
// socket — emit is the dispatcher's single-writer progress sender. Object
// absence is not an error: the watch simply yields no Added event.
//
// TODO(rbac): watch_status needs the watch verb on the managed CR groups; see
// the note on Apply.
func (k *KubeExecutor) WatchStatus(ctx context.Context, ref ResourceRef, maxSnapshots int, emit func(stage string, snap StatusSnapshot)) (WatchResult, error) {
	if maxSnapshots < 0 {
		return WatchResult{}, badRequest(fmt.Errorf("max_snapshots must be >= 0, got %d", maxSnapshots))
	}
	switch {
	case maxSnapshots == 0:
		maxSnapshots = defaultWatchSnapshots
	case maxSnapshots > maxWatchSnapshots:
		maxSnapshots = maxWatchSnapshots
	}

	ri, err := k.resolve(ref)
	if err != nil {
		return WatchResult{}, err
	}
	w, err := ri.Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + ref.Name})
	if err != nil {
		return WatchResult{}, fmt.Errorf("watch %s/%s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
	}
	defer w.Stop()

	sent := 0
	for {
		select {
		case <-ctx.Done():
			return WatchResult{SnapshotsSent: sent, Reason: WatchReasonDeadline}, nil
		case event, ok := <-w.ResultChan():
			if !ok {
				return WatchResult{SnapshotsSent: sent, Reason: WatchReasonClosed}, nil
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				continue // skip Deleted/Bookmark/Error frames — only status snapshots
			}
			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue // a non-unstructured (e.g. Status) object carries no snapshot
			}
			snap, err := snapshotOf(obj)
			if err != nil {
				return WatchResult{SnapshotsSent: sent, Reason: WatchReasonClosed}, err
			}
			emit(eventStage(event.Type), snap)
			sent++
			if sent >= maxSnapshots {
				return WatchResult{SnapshotsSent: sent, Reason: WatchReasonMaxSnapshots}, nil
			}
		}
	}
}

// snapshotOf builds a found StatusSnapshot from an unstructured object: its
// resourceVersion, generation, and .status subobject (omitted when absent).
func snapshotOf(obj *unstructured.Unstructured) (StatusSnapshot, error) {
	snap := StatusSnapshot{
		Found:           true,
		ResourceVersion: obj.GetResourceVersion(),
		Generation:      obj.GetGeneration(),
	}
	status, found, err := unstructured.NestedMap(obj.Object, "status")
	if err != nil {
		return StatusSnapshot{}, fmt.Errorf("read .status of %s: %w", obj.GetName(), err)
	}
	if found {
		raw, err := json.Marshal(status)
		if err != nil {
			return StatusSnapshot{}, fmt.Errorf("marshal .status of %s: %w", obj.GetName(), err)
		}
		snap.Status = raw
	}
	return snap, nil
}

// eventStage maps a watch event type to the wire stage string ("added"/
// "modified"). Only Added/Modified reach it (see WatchStatus's filter).
func eventStage(t watch.EventType) string {
	if t == watch.Added {
		return "added"
	}
	return "modified"
}

// parsePropagationPolicy validates a propagation_policy param against the three
// legal values, returning a BAD_REQUEST-class error otherwise.
func parsePropagationPolicy(p string) (metav1.DeletionPropagation, error) {
	switch metav1.DeletionPropagation(p) {
	case metav1.DeletePropagationForeground:
		return metav1.DeletePropagationForeground, nil
	case metav1.DeletePropagationBackground:
		return metav1.DeletePropagationBackground, nil
	case metav1.DeletePropagationOrphan:
		return metav1.DeletePropagationOrphan, nil
	default:
		return "", badRequest(fmt.Errorf("invalid propagation_policy %q (want Foreground|Background|Orphan)", p))
	}
}
