package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
)

const backendFinalizer = "registry.opencloud.wso2.com/backend-cleanup"

// RegistryBackendReconciler provisions one Harbor per tenant. The CR is the
// source of truth; Harbor is installed via Helm from inside the reconcile loop
// and its readiness is polled with RequeueAfter.
type RegistryBackendReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Helm     *helm.Deployer
	HelmCfg  config.HelmConfig
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends;registryinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/status;registryinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/finalizers;registryinstances/finalizers,verbs=update
//
// The operator emits Events and reads namespaces:
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//
// Harbor's chart objects are created by Helm using this pod's ServiceAccount,
// so the ClusterRole must grant the resource types the Harbor chart manages:
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;serviceaccounts;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

func (r *RegistryBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get RegistryBackend: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &cr, log)
	}
	if !controllerutil.ContainsFinalizer(&cr, backendFinalizer) {
		controllerutil.AddFinalizer(&cr, backendFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	tenantID := cr.Spec.TenantID
	plan := cr.Spec.Plan
	if plan == "" {
		plan = "starter"
	}

	// 1. Ensure Harbor admin + db passwords exist in an owned Secret.
	adminPass, dbPass, secretName, err := r.ensureAdminSecret(ctx, &cr)
	if err != nil {
		return r.transient(ctx, &cr, "", "ensure admin secret", err)
	}

	// 2. Render values + install Harbor (idempotent: Install skips if the
	// release already exists).
	tplan, err := helm.PlanFor(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	values, err := helm.GenerateValues(helm.ValuesInput{
		TenantID:     tenantID,
		AdminPass:    adminPass,
		DBPass:       dbPass,
		BaseDomain:   r.HelmCfg.BaseDomain,
		StorageClass: r.HelmCfg.StorageClass,
		IngressClass: r.HelmCfg.IngressClass,
		CertIssuer:   r.HelmCfg.CertIssuer,
		Plan:         tplan,
	})
	if err != nil {
		return r.fail(ctx, &cr, "generate values", err)
	}
	if err := r.Helm.Install(ctx, tenantID, cr.Namespace, values); err != nil {
		return r.transient(ctx, &cr, secretName, "helm install", err)
	}

	// 3. Wait for Harbor to accept API requests (poll, don't block).
	registryURL := fmt.Sprintf("https://registry.%s.%s", tenantID, r.HelmCfg.BaseDomain)
	cli := r.harborClient(registryURL, adminPass)
	if err := cli.Ping(ctx); err != nil {
		return r.provisioning(ctx, &cr, secretName, registryURL,
			"waiting for Harbor to accept API requests", 15*time.Second)
	}

	// 4. Apply Harbor system configuration (idempotent).
	if err := cli.Configure(ctx); err != nil {
		return r.provisioning(ctx, &cr, secretName, registryURL,
			fmt.Sprintf("configuring Harbor: %v", err), 15*time.Second)
	}

	// 5. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.RegistryURL = registryURL
		s.AdminSecretName = secretName
		s.Message = "Harbor is running"
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "Harbor is running")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(&cr, corev1.EventTypeNormal, reasonReady, "Harbor ready at "+registryURL)

	// Steady-state: re-check every minute to catch drift (Helm re-install is a
	// no-op, Ping confirms Harbor is still up).
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// ensureAdminSecret returns the Harbor admin + db passwords, creating an owned
// Secret with fresh random values on first reconcile. On later reconciles it
// returns the stored values so Helm re-installs are stable.
func (r *RegistryBackendReconciler) ensureAdminSecret(ctx context.Context, cr *registryv1alpha1.RegistryBackend) (admin, db, name string, err error) {
	name = cr.Name + "-harbor-admin"
	key := client.ObjectKey{Namespace: cr.Namespace, Name: name}

	var sec corev1.Secret
	if err = r.Get(ctx, key, &sec); err == nil {
		return string(sec.Data["admin-password"]), string(sec.Data["db-password"]), name, nil
	} else if !apierrors.IsNotFound(err) {
		return "", "", "", err
	}

	if admin, err = genPassword(); err != nil {
		return "", "", "", err
	}
	if db, err = genPassword(); err != nil {
		return "", "", "", err
	}
	sec = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"admin-password": []byte(admin),
			"db-password":    []byte(db),
		},
	}
	if err = controllerutil.SetControllerReference(cr, &sec, r.Scheme); err != nil {
		return "", "", "", err
	}
	if err = r.Create(ctx, &sec); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Lost a race; re-read the winner's values.
			if e := r.Get(ctx, key, &sec); e == nil {
				return string(sec.Data["admin-password"]), string(sec.Data["db-password"]), name, nil
			}
		}
		return "", "", "", err
	}
	return admin, db, name, nil
}

// handleDelete runs the Backend finalizer: refuse while RegistryInstances still
// reference this Backend, then uninstall Harbor and (optionally) its PVCs.
func (r *RegistryBackendReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.RegistryBackend, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, backendFinalizer) {
		return ctrl.Result{}, nil
	}

	// Dependent guard.
	var instances registryv1alpha1.RegistryInstanceList
	if err := r.List(ctx, &instances); err != nil {
		return ctrl.Result{}, fmt.Errorf("list RegistryInstances: %w", err)
	}
	var blockers []string
	for _, in := range instances.Items {
		if in.Spec.BackendRef.Name == cr.Name && in.Spec.BackendRef.Namespace == cr.Namespace {
			blockers = append(blockers, in.Namespace+"/"+in.Name)
		}
	}
	if len(blockers) > 0 {
		msg := fmt.Sprintf("blocked by %d RegistryInstance(s): %v — delete them first", len(blockers), blockers)
		r.Recorder.Event(cr, corev1.EventTypeWarning, reasonBlocked, msg)
		_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
			s.Phase = phaseTerminating
			s.Message = msg
			setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonBlocked, msg)
		})
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Uninstall Harbor.
	if err := r.Helm.Uninstall(ctx, cr.Spec.TenantID, cr.Namespace); err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, fmt.Errorf("helm uninstall: %w", err)
	}

	// Reclaim data if requested (Helm keeps PVCs via resourcePolicy: keep).
	if cr.Spec.ReclaimPolicy == reclaimDelete {
		if err := r.deletePVCs(ctx, cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete pvcs: %w", err)
		}
		log.Info("reclaimed Harbor PVCs", "tenant", cr.Spec.TenantID)
	}

	controllerutil.RemoveFinalizer(cr, backendFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deletePVCs removes the Harbor release's PVCs (best-effort, by the Helm
// instance label).
func (r *RegistryBackendReconciler) deletePVCs(ctx context.Context, cr *registryv1alpha1.RegistryBackend) error {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs,
		client.InNamespace(cr.Namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": "harbor-" + cr.Spec.TenantID},
	); err != nil {
		return err
	}
	for i := range pvcs.Items {
		if err := r.Delete(ctx, &pvcs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *RegistryBackendReconciler) harborClient(url, adminPass string) *harbor.Client {
	if r.HelmCfg.InsecureHarborTLS {
		return harbor.NewInsecureClient(url, adminPass)
	}
	return harbor.NewClient(url, adminPass)
}

// --- status helpers ---

func (r *RegistryBackendReconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryBackendStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.RegistryBackend
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		if err := r.Status().Update(ctx, &fresh); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("status update: too many conflicts")
}

// provisioning is an expected wait (Harbor still booting): sets Provisioning
// and requeues after the delay with no error (so no error log / backoff).
func (r *RegistryBackendReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.RegistryBackend, secretName, url, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		if secretName != "" {
			s.AdminSecretName = secretName
		}
		if url != "" {
			s.RegistryURL = url
		}
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

// transient is an unexpected external error: sets Provisioning and returns the
// error so controller-runtime applies exponential backoff.
func (r *RegistryBackendReconciler) transient(ctx context.Context, cr *registryv1alpha1.RegistryBackend, secretName, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonTransient, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		if secretName != "" {
			s.AdminSecretName = secretName
		}
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonTransient, msg)
	})
	return ctrl.Result{}, cause
}

// fail is a terminal spec error: sets Failed and returns the error.
func (r *RegistryBackendReconciler) fail(ctx context.Context, cr *registryv1alpha1.RegistryBackend, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonError, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseFailed
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonError, msg)
	})
	return ctrl.Result{}, cause
}

func (r *RegistryBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryBackend{}).
		Owns(&corev1.Secret{}).
		Named("registrybackend").
		Complete(r)
}
