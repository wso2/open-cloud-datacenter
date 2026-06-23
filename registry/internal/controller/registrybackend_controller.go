package controller

import (
	"context"
	"errors"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
)

const backendFinalizerName = "registry.opencloud.wso2.com/backend-finalizer"

// Phase constants — title case to match KVI pattern and Kubernetes conventions.
const (
	PhasePending      = "Pending"
	PhaseProvisioning = "Provisioning"
	PhaseReady        = "Ready"
	PhaseFailed       = "Failed"
	PhaseTerminating  = "Terminating"
)

// dbStatusToPhase maps the internal DB deployment status (ALL CAPS) to the
// title-case phase values the CR status.phase field uses.
func dbStatusToPhase(s db.RegistryStatus) string {
	switch s {
	case db.StatusPending:
		return PhasePending
	case db.StatusDeploying:
		return PhaseProvisioning
	case db.StatusReady:
		return PhaseReady
	case db.StatusFailed:
		return PhaseFailed
	case db.StatusDeleting, db.StatusDeleted:
		return PhaseTerminating
	default:
		return string(s)
	}
}

// setBackendReadyCondition upserts the "Ready" condition on the backend status.
// Only updates LastTransitionTime when the Status changes, matching Kubernetes conventions.
func setBackendReadyCondition(s *registryv1alpha1.RegistryBackendStatus, status metav1.ConditionStatus, reason, message string) {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
	for i, c := range s.Conditions {
		if c.Type == "Ready" {
			if c.Status == status {
				return
			}
			s.Conditions[i] = cond
			return
		}
	}
	s.Conditions = append(s.Conditions, cond)
}

// RegistryBackendReconciler reconciles RegistryBackend CRs.
//
// Each RegistryBackend represents one Harbor Helm deployment for a tenant.
// dc-api creates a RegistryBackend CR when the first datacenter project in a
// tenant requests a container registry. The deploy_worker picks up the
// registry_deployments DB row and drives the 7-step Harbor provisioning flow.
// This controller syncs the resulting DB state back into CR status so dc-api
// (via RegistryInstance status) and kubectl can observe deployment progress.
type RegistryBackendReconciler struct {
	client.Client
	Store *db.Store
}

func (r *RegistryBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var backend registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, req.NamespacedName, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	tenantID := backend.Spec.TenantID
	if tenantID == "" {
		log.Info("RegistryBackend has empty tenantID — skipping")
		return ctrl.Result{}, nil
	}

	// ── Deletion path ──────────────────────────────────────────────────────────
	if !backend.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&backend, backendFinalizerName) {
			dep, _ := r.Store.GetDeployment(ctx, tenantID)
			if dep != nil && dep.Status != db.StatusDeleting && dep.Status != db.StatusDeleted {
				if err := r.Store.UpdateForDelete(ctx, tenantID, true); err != nil {
					log.Error(err, "mark backend for delete failed", "tenant", tenantID)
					return ctrl.Result{}, err
				}
			}
			// Wait for the worker to finish teardown
			if dep != nil && dep.Status == db.StatusDeleting {
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
			controllerutil.RemoveFinalizer(&backend, backendFinalizerName)
			return ctrl.Result{}, r.Update(ctx, &backend)
		}
		return ctrl.Result{}, nil
	}

	// ── Add finalizer on first reconcile ───────────────────────────────────────
	if !controllerutil.ContainsFinalizer(&backend, backendFinalizerName) {
		controllerutil.AddFinalizer(&backend, backendFinalizerName)
		if err := r.Update(ctx, &backend); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// ── Ensure registry_deployments row exists for this tenant ─────────────────
	dep, err := r.Store.GetDeployment(ctx, tenantID)
	if err != nil {
		return ctrl.Result{}, err
	}
	if dep == nil {
		plan := backend.Spec.Plan
		if plan == "" {
			plan = "starter"
		}
		if err := r.Store.CreateDeployment(ctx, &db.RegistryDeployment{
			TenantID:    tenantID,
			Namespace:   tenantID + "-management",
			Status:      db.StatusPending,
			HelmRelease: "harbor-" + tenantID,
			Plan:        plan,
		}); err != nil {
			if dep2, _ := r.Store.GetDeployment(ctx, tenantID); dep2 != nil {
				dep = dep2
			} else {
				return ctrl.Result{}, err
			}
		} else {
			log.Info("created harbor deployment record", "tenant", tenantID)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// ── Sync deployment status → CR status ─────────────────────────────────────
	phase := dbStatusToPhase(dep.Status)
	if err := r.updateStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phase
		s.RegistryURL = dep.RegistryURL
		s.Progress = dep.Progress
		s.Message = dep.ErrorMessage
		switch dep.Status {
		case db.StatusReady:
			setBackendReadyCondition(s, metav1.ConditionTrue, "HarborDeployed", "Harbor Helm deployment complete")
		case db.StatusFailed:
			setBackendReadyCondition(s, metav1.ConditionFalse, "HarborFailed", dep.ErrorMessage)
		case db.StatusDeleting, db.StatusDeleted:
			setBackendReadyCondition(s, metav1.ConditionFalse, "HarborTerminating", "Harbor is being torn down")
		default:
			setBackendReadyCondition(s, metav1.ConditionFalse, "HarborProvisioning", "Harbor Helm deployment in progress")
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	switch dep.Status {
	case db.StatusReady, db.StatusFailed, db.StatusDeleted:
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// updateStatus fetches the freshest CR, applies the mutator, and Status-Updates it.
// Retries once on ResourceVersion conflict.
func (r *RegistryBackendReconciler) updateStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryBackendStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.RegistryBackend
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		err := r.Status().Update(ctx, &fresh)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return errors.New("registrybackend status update: too many conflicts")
}

func (r *RegistryBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only reconcile CRs that carry our dc-api label. This prevents our controller
	// from touching RegistryBackend CRs created by other operators in the same cluster.
	ownedByUs := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, ok := obj.GetLabels()["dc-api.wso2.com/tenant"]
		return ok
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryBackend{}, builder.WithPredicates(ownedByUs)).
		Named("registrybackend").
		Complete(r)
}
