package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// tenantProjectKey is the metadata key Rancher and Harvester set on a namespace
// to record the project owning it. Its value is "<cluster-id>:<project-id>".
const tenantProjectKey = "field.cattle.io/projectId"

// tenantForNamespace returns the tenant that owns a namespace, read from the
// project Rancher assigned it to.
//
// The tenant is never taken from a Registry's spec. A namespace's project
// membership is controlled by Rancher, so deriving from it means a Registry
// cannot name a tenant it does not belong to — there is no field to falsify.
// Namespaces outside any project have no tenant, and the caller waits rather
// than guessing one.
func (r *RegistryReconciler) tenantForNamespace(ctx context.Context, name string) (string, error) {
	var ns corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &ns); err != nil {
		return "", fmt.Errorf("get namespace %s: %w", name, err)
	}

	raw := ns.Annotations[tenantProjectKey]
	if raw == "" {
		raw = ns.Labels[tenantProjectKey]
	}
	if raw == "" {
		return "", fmt.Errorf("namespace %s is not assigned to a Harvester project (no %s)", name, tenantProjectKey)
	}

	tenant, err := tenantIDFromProject(raw)
	if err != nil {
		return "", fmt.Errorf("namespace %s: %w", name, err)
	}
	return tenant, nil
}

// tenantIDFromProject extracts the project ID from a "<cluster-id>:<project-id>"
// value. The project ID alone identifies the tenant, and unlike a project's
// display name it never changes.
func tenantIDFromProject(raw string) (string, error) {
	id := raw
	if _, after, found := strings.Cut(raw, ":"); found {
		id = after
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("project reference %q has no project ID", raw)
	}
	return strings.ToLower(id), nil
}

// backendNameForTenant returns the name of the RegistryBackend serving a
// tenant. It is derived rather than stored so that concurrent first-time
// Registries in the same tenant resolve to one name, letting the API server
// settle the race instead of the controller.
func backendNameForTenant(tenant string) string {
	return "rb-" + tenant
}
