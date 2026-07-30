package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create
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

	// 1. Ensure the full pinned Harbor credential set exists in an owned Secret
	// (admin + db passwords and the chart's internal keys — pinned so that
	// re-rendered values are deterministic and upgrades never rotate secrets).
	secrets, secretName, err := r.ensureAdminSecret(ctx, &cr)
	if err != nil {
		return r.transient(ctx, &cr, "", "ensure admin secret", err)
	}

	// 2. Ensure the tenant's Harbor namespace (<tenantID>-management) exists,
	// then render values and converge the Helm release (install when missing,
	// upgrade when the desired values drift from the deployed ones — e.g. a
	// plan change; no-op otherwise).
	harborNS := harborNamespace(tenantID)
	if err := r.ensureNamespace(ctx, harborNS); err != nil {
		return r.transient(ctx, &cr, secretName, "ensure Harbor namespace", err)
	}
	tplan, err := helm.PlanFor(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	values, err := helm.GenerateValues(helm.ValuesInput{
		TenantID:      tenantID,
		SecretName:    secretName, // core/jobservice/registry secrets + admin password read from here via existingSecret
		DBPass:        secrets.DBPass,
		EncryptionKey: secrets.EncryptionKey,
		BaseDomain:    r.HelmCfg.BaseDomain,
		StorageClass:  r.HelmCfg.StorageClass,
		IngressClass:  r.HelmCfg.IngressClass,
		CertIssuer:    r.HelmCfg.CertIssuer,
		Plan:          tplan,
	})
	if err != nil {
		return r.fail(ctx, &cr, "generate values", err)
	}
	if err := r.Helm.Install(ctx, tenantID, harborNS, values); err != nil {
		return r.transient(ctx, &cr, secretName, "helm install/upgrade", err)
	}

	// 2b. Grow plan-controlled PVCs to the plan's sizes (helm cannot resize
	// existing claims; PVC expansion is a direct, grow-only patch).
	if err := r.ensureStorageSize(ctx, &cr, harborNS, tplan); err != nil {
		return r.transient(ctx, &cr, secretName, "ensure storage size", err)
	}

	// 3. Wait for Harbor to accept API requests (poll, don't block).
	registryURL := fmt.Sprintf("https://registry.%s.%s", tenantID, r.HelmCfg.BaseDomain)
	cli := r.harborClient(registryURL, secrets.AdminPass)
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

// harborSecrets holds every credential the Harbor chart must receive as a
// pinned value. If any were left unset, the chart would generate fresh random
// ones on every render — making helm upgrade rotate live credentials. Pinning
// them keeps GenerateValues deterministic, which is what makes upgrades safe.
type harborSecrets struct {
	AdminPass        string
	DBPass           string
	CoreSecret       string
	JobserviceSecret string
	RegistrySecret   string
	XSRFKey          string
	EncryptionKey    string
}

// harborSecretGenerators maps Secret data keys to their generators. Keys for
// admin-password, core.secret, core.xsrfKey, jobservice.secret, and
// registry.secret use the EXACT names Harbor's chart requires when read via
// existingSecret (HARBOR_ADMIN_PASSWORD/secret/CSRF_KEY/JOBSERVICE_SECRET/
// REGISTRY_HTTP_SECRET — see values_generator.go's harborValuesTemplate),
// so the same Secret this map builds is what the chart reads directly; their
// plaintext never enters values.yaml or the Helm release record. db-password
// and encryption-key have no existingSecret equivalent in the chart, so they
// stay under plain names and are still passed as raw template values.
// Lengths follow the Harbor chart's documented requirements (secretKey/
// core.secret/jobservice.secret/registry.secret: 16 chars; xsrfKey: 32).
var harborSecretGenerators = map[string]func() (string, error){
	"HARBOR_ADMIN_PASSWORD": genPassword,
	"db-password":           genPassword,
	"secret":                func() (string, error) { return genAlphaNum(16) }, // core.secret
	"JOBSERVICE_SECRET":     func() (string, error) { return genAlphaNum(16) },
	"REGISTRY_HTTP_SECRET":  func() (string, error) { return genAlphaNum(16) },
	"CSRF_KEY":              func() (string, error) { return genAlphaNum(32) }, // core.xsrfKey
	"encryption-key":        func() (string, error) { return genAlphaNum(16) },
}

// ensureAdminSecret returns the full pinned Harbor credential set, creating an
// owned Secret with fresh random values on first reconcile and returning the
// stored values on later reconciles so Helm renders stay stable. Any key not
// yet present (e.g. one added to harborSecretGenerators after this Secret was
// first created) is generated and added on a later reconcile; existing keys
// are never overwritten, so live credentials are never silently rotated.
func (r *RegistryBackendReconciler) ensureAdminSecret(ctx context.Context, cr *registryv1alpha1.RegistryBackend) (harborSecrets, string, error) {
	name := cr.Name + "-harbor-admin"
	key := client.ObjectKey{Namespace: cr.Namespace, Name: name}

	var sec corev1.Secret
	err := r.Get(ctx, key, &sec)
	switch {
	case err == nil:
		// Self-heal: generate any key not yet present.
		added := false
		for k, gen := range harborSecretGenerators {
			if len(sec.Data[k]) > 0 {
				continue
			}
			v, gerr := gen()
			if gerr != nil {
				return harborSecrets{}, "", gerr
			}
			if sec.Data == nil {
				sec.Data = map[string][]byte{}
			}
			sec.Data[k] = []byte(v)
			added = true
		}
		if added {
			if uerr := r.Update(ctx, &sec); uerr != nil {
				return harborSecrets{}, "", uerr
			}
		}
	case apierrors.IsNotFound(err):
		sec = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cr.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{},
		}
		for k, gen := range harborSecretGenerators {
			v, gerr := gen()
			if gerr != nil {
				return harborSecrets{}, "", gerr
			}
			sec.Data[k] = []byte(v)
		}
		if cerr := controllerutil.SetControllerReference(cr, &sec, r.Scheme); cerr != nil {
			return harborSecrets{}, "", cerr
		}
		if cerr := r.Create(ctx, &sec); cerr != nil {
			if !apierrors.IsAlreadyExists(cerr) {
				return harborSecrets{}, "", cerr
			}
			// Lost a race; re-read the winner's values.
			if gerr := r.Get(ctx, key, &sec); gerr != nil {
				return harborSecrets{}, "", gerr
			}
		}
	default:
		return harborSecrets{}, "", err
	}

	return harborSecrets{
		AdminPass:        string(sec.Data["HARBOR_ADMIN_PASSWORD"]),
		DBPass:           string(sec.Data["db-password"]),
		CoreSecret:       string(sec.Data["secret"]),
		JobserviceSecret: string(sec.Data["JOBSERVICE_SECRET"]),
		RegistrySecret:   string(sec.Data["REGISTRY_HTTP_SECRET"]),
		XSRFKey:          string(sec.Data["CSRF_KEY"]),
		EncryptionKey:    string(sec.Data["encryption-key"]),
	}, name, nil
}

// ensureStorageSize grows the plan-controlled PVCs to the plan's sizes. Helm
// cannot do this (StatefulSet volumeClaimTemplates are immutable and PVC size
// changes are frozen out of the upgrade diff), so the reconciler patches the
// claims directly. Grow-only: Kubernetes forbids shrinking, and the CRD's CEL
// rule already rejects plan downgrades at admission. Idempotent — equal or
// larger current size is a no-op. Requires a StorageClass with
// allowVolumeExpansion (Longhorn: yes).
func (r *RegistryBackendReconciler) ensureStorageSize(ctx context.Context, cr *registryv1alpha1.RegistryBackend, namespace string, plan helm.TenantPlan) error {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list PVCs: %w", err)
	}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		// The namespace is dedicated to this tenant's Harbor release, so name
		// suffixes are unambiguous: the registry image store and the database.
		var want string
		switch {
		case strings.HasSuffix(pvc.Name, "-registry"):
			want = plan.RegistryStorage
		case strings.Contains(pvc.Name, "-database-"):
			want = plan.DBStorage
		default:
			continue // jobservice/redis/trivy sizes are not plan-controlled
		}
		desired, err := resource.ParseQuantity(want)
		if err != nil {
			return fmt.Errorf("parse plan size %q: %w", want, err)
		}
		current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if desired.Cmp(current) <= 0 {
			continue // grow-only
		}
		if pvc.Spec.Resources.Requests == nil {
			pvc.Spec.Resources.Requests = corev1.ResourceList{}
		}
		pvc.Spec.Resources.Requests[corev1.ResourceStorage] = desired
		if err := r.Update(ctx, pvc); err != nil {
			return fmt.Errorf("expand PVC %s to %s: %w", pvc.Name, want, err)
		}
		r.Recorder.Event(cr, corev1.EventTypeNormal, "StorageExpanded",
			fmt.Sprintf("PVC %s: %s -> %s", pvc.Name, current.String(), want))
	}
	return nil
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

	// Uninstall Harbor from the tenant's Harbor namespace.
	if err := r.Helm.Uninstall(ctx, cr.Spec.TenantID, harborNamespace(cr.Spec.TenantID)); err != nil {
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
		client.InNamespace(harborNamespace(cr.Spec.TenantID)),
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

// harborNamespace is the namespace a tenant's Harbor is deployed into:
// "<tenantID>-management".
func harborNamespace(tenantID string) string {
	return tenantID + "-management"
}

// ensureNamespace creates the namespace if it does not already exist.
func (r *RegistryBackendReconciler) ensureNamespace(ctx context.Context, name string) error {
	var ns corev1.Namespace
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &ns); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := r.Create(ctx, &ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
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
	// TerminalError: no requeue (retrying cannot fix a spec-level error — only
	// a user edit can, and that edit re-triggers reconcile via the watch), but
	// unlike returning nil the failure still lands in logs and the
	// terminal_reconcile_errors_total metric.
	return ctrl.Result{}, reconcile.TerminalError(cause)
}

func (r *RegistryBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryBackend{}).
		Owns(&corev1.Secret{}).
		Named("registrybackend").
		Complete(r)
}
