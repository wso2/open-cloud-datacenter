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

const finalizerName = "registry.opencloud.wso2.com/finalizer"

// dbProjectStatusToPhase maps the internal DB project status (ALL CAPS) to the
// title-case phase values the CR status.phase field uses.
func dbProjectStatusToPhase(s string) string {
	switch s {
	case string(db.StatusPending):
		return PhasePending
	case string(db.StatusReady):
		return PhaseReady
	case string(db.StatusFailed):
		return PhaseFailed
	default:
		return s
	}
}

// setInstanceReadyCondition upserts the "Ready" condition on the instance status.
// Only updates LastTransitionTime when the Status changes, matching Kubernetes conventions.
func setInstanceReadyCondition(s *registryv1alpha1.RegistryInstanceStatus, status metav1.ConditionStatus, reason, message string) {
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

// RegistryInstanceReconciler reconciles RegistryInstance CRs.
//
// Lifecycle:
//
//	Phase 1 — Wait for the per-tenant RegistryBackend to reach Ready.
//	           Mirror backend status into the Instance while waiting.
//	Phase 2 — Ensure a registry_projects row exists so the project_worker
//	           can create the Harbor project + robot account.
//	           Sync project status back into CR status.
type RegistryInstanceReconciler struct {
	client.Client
	Store *db.Store
}

func (r *RegistryInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var instance registryv1alpha1.RegistryInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// ── Deletion path ──────────────────────────────────────────────────────────
	if !instance.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&instance, finalizerName) {
			if instance.Spec.ProjectID != "" {
				_ = r.Store.DeleteProject(ctx, instance.Spec.TenantID, instance.Spec.ProjectID, instanceRegistryName(&instance))
			}
			controllerutil.RemoveFinalizer(&instance, finalizerName)
			return ctrl.Result{}, r.Update(ctx, &instance)
		}
		return ctrl.Result{}, nil
	}

	// ── Add finalizer on first reconcile ───────────────────────────────────────
	if !controllerutil.ContainsFinalizer(&instance, finalizerName) {
		controllerutil.AddFinalizer(&instance, finalizerName)
		if err := r.Update(ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// ── Phase 1: wait for RegistryBackend ──────────────────────────────────────
	// BackendRef is a required non-pointer field; both Name and Namespace are always set by dc-api.
	backendName := instance.Spec.BackendRef.Name
	backendNS := instance.Spec.BackendRef.Namespace

	var backend registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, client.ObjectKey{Name: backendName, Namespace: backendNS}, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			msg := "waiting for RegistryBackend " + backendNS + "/" + backendName
			_ = r.updateStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryInstanceStatus) {
				s.Phase = PhasePending
				s.Message = msg
				setInstanceReadyCondition(s, metav1.ConditionFalse, "BackendNotFound", msg)
			})
			log.Info("RegistryBackend not found; requeuing", "backend", backendNS+"/"+backendName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if backend.Status.Phase != PhaseReady {
		_ = r.updateStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryInstanceStatus) {
			s.Phase = backend.Status.Phase
			s.RegistryURL = backend.Status.RegistryURL
			s.Progress = backend.Status.Progress
			s.Message = backend.Status.Message
			setInstanceReadyCondition(s, metav1.ConditionFalse, "BackendNotReady",
				"RegistryBackend phase="+backend.Status.Phase)
		})
		log.Info("RegistryBackend not ready; requeuing", "backendPhase", backend.Status.Phase)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// ── Phase 2: ensure Harbor project row exists ──────────────────────────────
	if instance.Spec.ProjectID == "" {
		log.Info("RegistryInstance has no projectID — skipping Phase 2")
		return ctrl.Result{}, nil
	}

	rName := instanceRegistryName(&instance)
	proj, err := r.Store.GetProject(ctx, instance.Spec.TenantID, instance.Spec.ProjectID, rName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if proj == nil {
		if err := r.Store.CreateProject(ctx, &db.RegistryProject{
			TenantID:          instance.Spec.TenantID,
			ProjectID:         instance.Spec.ProjectID,
			RegistryName:      rName,
			HarborProjectName: rName,
		}); err != nil {
			if proj2, _ := r.Store.GetProject(ctx, instance.Spec.TenantID, instance.Spec.ProjectID, rName); proj2 != nil {
				proj = proj2
			} else {
				return ctrl.Result{}, err
			}
		} else {
			log.Info("created harbor project record", "tenant", instance.Spec.TenantID, "registry", rName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Sync project status → CR status.
	phase := dbProjectStatusToPhase(proj.Status)
	if err := r.updateStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phase
		s.RegistryURL = backend.Status.RegistryURL
		s.Message = proj.ErrorMessage
		switch proj.Status {
		case string(db.StatusReady):
			setInstanceReadyCondition(s, metav1.ConditionTrue, "HarborProjectReady", "Harbor project and robot account are ready")
		case string(db.StatusFailed):
			setInstanceReadyCondition(s, metav1.ConditionFalse, "HarborProjectFailed", proj.ErrorMessage)
		default:
			setInstanceReadyCondition(s, metav1.ConditionFalse, "HarborProjectProvisioning", "Harbor project creation in progress")
		}
	}); err != nil {
		return ctrl.Result{}, err
	}

	switch proj.Status {
	case string(db.StatusReady):
		return ctrl.Result{}, nil
	case string(db.StatusFailed):
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// instanceRegistryName returns the user-provided registry name from spec,
// falling back to the CR name if spec.registryName is empty.
func instanceRegistryName(instance *registryv1alpha1.RegistryInstance) string {
	if instance.Spec.RegistryName != "" {
		return instance.Spec.RegistryName
	}
	return instance.Name
}

// updateStatus fetches the freshest CR, applies the mutator, and Status-Updates it.
// Retries once on ResourceVersion conflict — enough because we only race against
// other controllers (kvi-controller-manager), which the label predicate now filters out.
func (r *RegistryInstanceReconciler) updateStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryInstanceStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.RegistryInstance
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
	return errors.New("registryinstance status update: too many conflicts")
}

func (r *RegistryInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only reconcile CRs stamped with our dc-api label. Prevents our controller
	// from touching RegistryInstance CRs created by other operators in the same cluster.
	ownedByUs := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, ok := obj.GetLabels()["dc-api.wso2.com/tenant"]
		return ok
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryInstance{}, builder.WithPredicates(ownedByUs)).
		Named("registryinstance").
		Complete(r)
}
