package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
)

const registryFinalizer = "registry.opencloud.wso2.com/registry-cleanup"

// RegistryReconciler serves one Registry: a project inside its tenant's Harbor,
// with credentials written to a Secret beside the Registry.
//
// The tenant comes from the namespace's Harvester project, so a Registry can
// only ever reach its own tenant's Harbor. The tenant's first Registry causes
// the Harbor deployment to be created; the rest reuse it.
type RegistryReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	HelmCfg  config.HelmConfig
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete

// Reconcile converges one Registry: bind a backend, then create its Harbor
// project, quota, robot account, and credentials Secret.
func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr registryv1alpha1.Registry
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Registry: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &cr, log)
	}
	if !controllerutil.ContainsFinalizer(&cr, registryFinalizer) {
		controllerutil.AddFinalizer(&cr, registryFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 1. Bind to the tenant's Harbor, creating it if this is the tenant's first
	// Registry, and wait until it serves API requests.
	backend, res, err := r.bindBackend(ctx, &cr)
	if backend == nil {
		return res, err
	}

	// 2. Read Harbor's admin password from the backend's Secret, which lives in
	// the Harbor namespace alongside the pods that consume it.
	adminPass, err := r.readSecretKey(ctx, backend.Status.HarborNamespace, backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return r.transient(ctx, &cr, "read Harbor admin secret", err)
	}
	registryURL := backend.Status.RegistryURL
	cli := r.harborClient(registryURL, adminPass)

	// 3. Create the Harbor project with this Registry's quota. Creation is
	// idempotent: Harbor answers 409 once the project exists.
	plan := cr.Spec.Plan
	if plan == "" {
		plan = planOrder[0]
	}
	// An unrecognized plan is a spec error, not transient — retrying can't fix it.
	quotaBytes, err := projectQuotaBytes(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	projectName := harborProjectName(&cr)
	if err := cli.CreateHarborProject(ctx, projectName, quotaBytes); err != nil {
		return r.transient(ctx, &cr, "create Harbor project", err)
	}

	// 3b. Converge the quota every reconcile — this is how a plan change takes
	// effect, and it doubles as drift detection.
	proj, err := cli.GetProject(ctx, projectName)
	if err != nil {
		return r.transient(ctx, &cr, "get Harbor project", err)
	}
	if proj.ProjectID == 0 {
		return r.transient(ctx, &cr, "get Harbor project", fmt.Errorf("Harbor returned a project with no project_id for %q", projectName))
	}
	if err := cli.EnsureProjectQuota(ctx, proj.ProjectID, quotaBytes); err != nil {
		// Transient, not terminal — a quota-below-usage rejection can resolve
		// once images are removed or the plan changes again.
		return r.transient(ctx, &cr, "set project quota", err)
	}

	// 4. Mint the robot account once and keep its credentials in a Secret.
	credName := credentialsSecretName(&cr)
	if err := r.ensureCredentials(ctx, &cr, cli, projectName, registryURL, credName); err != nil {
		return r.transient(ctx, &cr, "provision credentials", err)
	}

	// 5. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.TenantID = backend.Spec.TenantID
		s.BackendName = backend.Name
		s.HarborProject = projectName
		s.RegistryURL = registryURL
		s.CredentialsSecretName = credName
		s.Message = fmt.Sprintf("registry %q ready", projectName)
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "registry ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(&cr, corev1.EventTypeNormal, reasonReady, "registry ready; credentials in Secret "+credName)

	// Steady-state: re-check for drift (project deleted out-of-band, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// bindBackend resolves the Harbor serving this Registry's tenant, creating it if
// the tenant has none yet, and reports whether it is ready to accept projects.
// Returns (nil, result, err) when the caller should stop this pass.
func (r *RegistryReconciler) bindBackend(ctx context.Context, cr *registryv1alpha1.Registry) (*registryv1alpha1.RegistryBackend, ctrl.Result, error) {
	tenant, projectRef, err := r.tenantForNamespace(ctx, cr.Namespace)
	if err != nil {
		// Nothing identifies the tenant, so there is no Harbor this Registry
		// may use. Waiting is the only safe answer — guessing would hand it
		// another tenant's registry.
		res, _ := r.provisioning(ctx, cr, err.Error(), 30*time.Second)
		return nil, res, nil
	}

	name := backendNameForTenant(tenant)
	var backend registryv1alpha1.RegistryBackend
	err = r.Get(ctx, client.ObjectKey{Name: name}, &backend)

	switch {
	case apierrors.IsNotFound(err):
		// First Registry in this tenant: provision the Harbor deployment. The
		// name is derived from the tenant, so concurrent first Registries all
		// attempt the same object and the API server settles the race.
		desired := defaultBackendForTenant(name, tenant, projectRef)
		if cerr := r.Create(ctx, desired); cerr != nil {
			if !apierrors.IsAlreadyExists(cerr) {
				return nil, ctrl.Result{}, fmt.Errorf("create RegistryBackend %s: %w", name, cerr)
			}
			// Another Registry created it first; use theirs.
		} else {
			r.Recorder.Eventf(cr, corev1.EventTypeNormal, reasonProvisioning,
				"provisioning Harbor for tenant %s", tenant)
		}
		res, _ := r.provisioning(ctx, cr,
			fmt.Sprintf("provisioning Harbor for tenant %s; this takes a few minutes", tenant),
			15*time.Second)
		return nil, res, nil

	case err != nil:
		res, e := r.transient(ctx, cr, "get RegistryBackend", err)
		return nil, res, e
	}

	if backend.Status.Phase != phaseReady ||
		backend.Status.AdminSecretName == "" ||
		backend.Status.HarborNamespace == "" ||
		backend.Status.RegistryURL == "" {
		res, _ := r.provisioning(ctx, cr,
			fmt.Sprintf("Harbor for tenant %s is %s; waiting", tenant, phaseOrPending(backend.Status.Phase)),
			15*time.Second)
		return nil, res, nil
	}
	return &backend, ctrl.Result{}, nil
}

// defaultBackendForTenant builds the Harbor deployment created for a tenant's
// first Registry. It starts at the smallest plan and grows as the tenant's
// registries commit storage, and retains data so that removing the last
// Registry cannot destroy images.
func defaultBackendForTenant(name, tenant, projectRef string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{tenantLabel: tenant},
		},
		Spec: registryv1alpha1.RegistryBackendSpec{
			TenantID:      tenant,
			ProjectRef:    projectRef,
			Plan:          planOrder[0],
			ReclaimPolicy: reclaimRetain,
			Autoscale: registryv1alpha1.AutoscaleSpec{
				Enabled:                   true,
				CommittedThresholdPercent: defaultCommittedThresholdPercent,
			},
		},
	}
}

// phaseOrPending renders an unset phase readably.
func phaseOrPending(phase string) string {
	if phase == "" {
		return "starting"
	}
	return phase
}

// ensureCredentials mints a project robot account only if the credentials
// Secret does not already exist, then writes an owned Secret. Re-minting would
// invalidate credentials already in use, so it is done exactly once.
func (r *RegistryReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client, projectName, registryURL, credName string) error {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: credName}
	var existing corev1.Secret
	if err := r.Get(ctx, key, &existing); err == nil {
		return nil // already provisioned
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	robot, err := cli.CreateProjectRobotAccount(ctx, projectName, robotAccountName(cr))
	if err != nil {
		return fmt.Errorf("create robot account: %w", err)
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: credName, Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"robot_username": []byte(robot.Name),
			"robot_secret":   []byte(robot.Secret),
			"registry_url":   []byte(registryURL),
			"project":        []byte(projectName),
			"robot_id":       []byte(fmt.Sprintf("%d", robot.ID)),
		},
	}
	if err := controllerutil.SetControllerReference(cr, sec, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, sec); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// handleDelete runs the Registry finalizer, removing the Harbor project when
// reclaimPolicy is Delete. The credentials Secret is garbage-collected through
// its owner reference.
func (r *RegistryReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) (ctrl.Result, error) {
	// No finalizer means cleanup already finished and this is the last pass.
	if !controllerutil.ContainsFinalizer(cr, registryFinalizer) {
		return ctrl.Result{}, nil
	}

	if cr.Spec.ReclaimPolicy == reclaimDelete {
		if err := r.deleteHarborProject(ctx, cr, log); err != nil {
			// Keep the finalizer until the project is really gone, so that a
			// Harbor which is merely unreachable cannot cause images the tenant
			// asked to delete to be left behind. Only a missing backend is
			// accepted as nothing-to-do.
			if !apierrors.IsNotFound(err) {
				r.Recorder.Event(cr, corev1.EventTypeWarning, reasonTransient,
					"waiting to delete Harbor project: "+err.Error())
				return ctrl.Result{RequeueAfter: 15 * time.Second}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(cr, registryFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteHarborProject removes the Harbor project backing this Registry.
func (r *RegistryReconciler) deleteHarborProject(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) error {
	name := cr.Status.BackendName
	if name == "" {
		// Never bound, so no project was created.
		return nil
	}

	var backend registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, client.ObjectKey{Name: name}, &backend); err != nil {
		return err // NotFound → caller treats as nothing to clean up
	}
	if backend.Status.AdminSecretName == "" || backend.Status.HarborNamespace == "" {
		// Harbor was never provisioned far enough to hold a project.
		return nil
	}

	adminPass, err := r.readSecretKey(ctx, backend.Status.HarborNamespace, backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return err
	}

	projectName := cr.Status.HarborProject
	if projectName == "" {
		projectName = harborProjectName(cr)
	}
	log.Info("deleting Harbor project", "project", projectName)
	if err := r.harborClient(backend.Status.RegistryURL, adminPass).DeleteProject(ctx, projectName); err != nil {
		return fmt.Errorf("delete Harbor project: %w", err)
	}
	return nil
}

// readSecretKey returns one key's value from a Secret.
func (r *RegistryReconciler) readSecretKey(ctx context.Context, namespace, name, key string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("secret name is empty")
	}
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &sec); err != nil {
		return "", err
	}
	v, ok := sec.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return string(v), nil
}

// harborClient returns a Harbor client honouring the configured TLS mode.
func (r *RegistryReconciler) harborClient(url, adminPass string) *harbor.Client {
	return newHarborClient(r.HelmCfg, url, adminPass)
}

// --- naming ---

// harborProjectName returns the Harbor project for a Registry, as
// <namespace>-<name>.
//
// Both parts are DNS labels, so the result always satisfies Harbor's project
// naming rules, and because namespaces are unique cluster-wide no two
// Registries can ever resolve to the same project. That matters: Harbor answers
// 409 for an existing project and the operator treats it as success, so any
// collision would silently hand one namespace a robot account on another's
// images.
func harborProjectName(cr *registryv1alpha1.Registry) string {
	return strings.ToLower(cr.Namespace + "-" + cr.Name)
}

// credentialsSecretName returns the Secret holding this Registry's robot credentials.
func credentialsSecretName(cr *registryv1alpha1.Registry) string {
	return "registry-credentials-" + cr.Name
}

// robotAccountName returns the robot account name for a Registry. Harbor
// prefixes it with "robot$<project>+", so the suffix is kept short.
func robotAccountName(cr *registryv1alpha1.Registry) string {
	s := strings.ToLower(cr.Name)
	if len(s) > 12 {
		s = s[:12]
	}
	return "ci-" + s
}

// projectQuotaGi maps a plan to a registry's Harbor storage quota in gibibytes.
// This is separate from the backend's deployment sizing.
var projectQuotaGi = map[string]int64{
	"starter":      5,
	"professional": 20,
	"enterprise":   100,
}

// projectQuotaBytes resolves a plan to a storage quota in bytes. Plan-name
// validation delegates to helm.PlanFor, the single source of truth for valid
// plan names.
func projectQuotaBytes(plan string) (int64, error) {
	if _, err := helm.PlanFor(plan); err != nil {
		return 0, err
	}
	gi, ok := projectQuotaGi[plan]
	if !ok {
		// Shouldn't happen if projectQuotaGi and helm.plans stay in sync.
		return 0, fmt.Errorf("plan %q is valid but has no configured Harbor project quota", plan)
	}
	return gi * 1024 * 1024 * 1024, nil
}

// --- status helpers ---

// patchStatus applies mutate to the latest status, retrying once on conflict.
func (r *RegistryReconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.Registry
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

// provisioning records a wait state and requeues after the given delay.
func (r *RegistryReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.Registry, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

// transient records a retryable failure and returns the error for backoff.
func (r *RegistryReconciler) transient(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonTransient, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonTransient, msg)
	})
	return ctrl.Result{}, cause
}

// fail marks a spec error that retrying cannot resolve: sets Failed and stops
// requeueing. Only for causes traceable to the spec, since editing it
// re-triggers reconcile through the watch.
func (r *RegistryReconciler) fail(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonError, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseFailed
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonError, msg)
	})
	return ctrl.Result{}, reconcile.TerminalError(cause)
}

// SetupWithManager registers the controller with the manager. Registries also
// watch their backend, so they converge as soon as Harbor becomes ready instead
// of waiting out a requeue.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.Registry{}).
		Owns(&corev1.Secret{}).
		Watches(&registryv1alpha1.RegistryBackend{},
			handler.EnqueueRequestsFromMapFunc(r.registriesForBackend),
			builder.WithPredicates(backendReadinessChanged())).
		Named("registry").
		Complete(r)
}

// registriesForBackend maps a backend to the Registries bound to it.
func (r *RegistryReconciler) registriesForBackend(ctx context.Context, obj client.Object) []reconcile.Request {
	backend, ok := obj.(*registryv1alpha1.RegistryBackend)
	if !ok {
		return nil
	}
	var list registryv1alpha1.RegistryList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		reg := &list.Items[i]
		// Registries that have not bound yet are matched by tenant, so the
		// first Registry in a tenant still wakes when its Harbor comes up.
		if reg.Status.BackendName == backend.Name ||
			(reg.Status.BackendName == "" && reg.Status.TenantID == backend.Spec.TenantID) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(reg)})
		}
	}
	return out
}
