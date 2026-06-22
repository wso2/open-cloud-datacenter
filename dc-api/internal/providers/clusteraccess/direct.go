package clusteraccess

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Direct is the Accessor backed by a dynamic client on the master kubeconfig.
// Every method is the exact c.dynamic.Resource(gvr).Namespace(ns).<verb>(...)
// chain the providers run today, so a provider constructed with a Direct
// accessor behaves unchanged to the byte.
type Direct struct{ dyn dynamic.Interface }

// NewDirect wraps a dynamic client as a Direct accessor.
func NewDirect(dyn dynamic.Interface) *Direct { return &Direct{dyn: dyn} }

// Ensure Direct satisfies Accessor at compile time.
var _ Accessor = (*Direct)(nil)

func (d *Direct) Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error) {
	return d.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, opts)
}

func (d *Direct) List(ctx context.Context, gvr schema.GroupVersionResource, ns string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return d.dyn.Resource(gvr).Namespace(ns).List(ctx, opts)
}

func (d *Direct) Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return d.dyn.Resource(gvr).Namespace(ns).Create(ctx, obj, opts)
}

func (d *Direct) Apply(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, fieldManager string, force bool) (*unstructured.Unstructured, error) {
	return d.dyn.Resource(gvr).Namespace(ns).Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: fieldManager, Force: force})
}

func (d *Direct) Update(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return d.dyn.Resource(gvr).Namespace(ns).Update(ctx, obj, opts)
}

func (d *Direct) Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, opts metav1.DeleteOptions) error {
	return d.dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, opts)
}
