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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
)

const instanceFinalizer = "registry.opencloud.wso2.com/instance-cleanup"

// RegistryInstanceReconciler provisions one Harbor project + robot account per
// registry. The robot credentials are written into an owner-referenced K8s
// Secret; dc-api reads that Secret directly (no DB, no HTTP gateway).
type RegistryInstanceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	HelmCfg  config.HelmConfig
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registryinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete

// Reconcile converges one Harbor project: quota, robot account, and the
// credentials Secret, once the referenced Backend is Ready.
func (r *RegistryInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr registryv1alpha1.RegistryInstance
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get RegistryInstance: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &cr, log)
	}
	if !controllerutil.ContainsFinalizer(&cr, instanceFinalizer) {
		controllerutil.AddFinalizer(&cr, instanceFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 1. Resolve the Backend and wait for it to be Ready.
	backend, res, err := r.resolveBackend(ctx, &cr)
	if backend == nil {
		return res, err
	}

	// 2. Read the Backend's admin password. Key name matches what the Backend
	// controller stores it under — HARBOR_ADMIN_PASSWORD, the exact key Harbor's
	// chart itself requires when read via existingSecretAdminPassword.
	adminPass, err := r.readSecretKey(ctx, harborNamespace(backend.Spec.TenantID), backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return r.transient(ctx, &cr, "read backend admin secret", err)
	}
	registryURL := backend.Status.RegistryURL
	cli := r.harborClient(registryURL, adminPass)

	// 3. Create the Harbor project with its plan's storage quota (idempotent
	// — 409 treated as success on creation).
	plan := cr.Spec.Plan
	if plan == "" {
		plan = "starter"
	}
	// An unrecognized plan is a spec error, not transient — retrying can't fix it.
	quotaBytes, err := projectQuotaBytes(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	projectName := projectNameFor(&cr)
	if err := cli.CreateHarborProject(ctx, projectName, quotaBytes); err != nil {
		return r.transient(ctx, &cr, "create Harbor project", err)
	}

	// 3b. Converge the project's quota every reconcile — this is how a plan
	// change takes effect, and it doubles as drift detection.
	proj, err := cli.GetProject(ctx, projectName)
	if err != nil {
		return r.transient(ctx, &cr, "get Harbor project", err)
	}
	if proj.ProjectID == 0 {
		return r.transient(ctx, &cr, "get Harbor project", fmt.Errorf("Harbor returned a project with no project_id for %q", projectName))
	}
	if err := cli.EnsureProjectQuota(ctx, proj.ProjectID, quotaBytes); err != nil {
		// Transient, not terminal — a quota-below-usage rejection can resolve
		// once the tenant frees space or the plan changes again.
		return r.transient(ctx, &cr, "set project quota", err)
	}

	// 4. Mint the robot account ONCE and persist creds in an owned Secret.
	credName := credentialsSecretName(&cr)
	if err := r.ensureCredentials(ctx, &cr, cli, projectName, registryURL, credName); err != nil {
		return r.transient(ctx, &cr, "provision credentials", err)
	}

	// 5. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.RegistryURL = registryURL
		s.CredentialsSecretName = credName
		s.Message = fmt.Sprintf("Harbor project %q ready", projectName)
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "registry ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(&cr, corev1.EventTypeNormal, reasonReady, "registry ready; credentials in Secret "+credName)

	// Steady-state: re-check for drift (project deleted out-of-band, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// resolveBackend fetches the referenced Backend. Returns (nil, result, err)
// with a requeue when the caller should stop this pass (backend missing / not
// ready); returns (backend, _, nil) when it is Ready.
func (r *RegistryInstanceReconciler) resolveBackend(ctx context.Context, cr *registryv1alpha1.RegistryInstance) (*registryv1alpha1.RegistryBackend, ctrl.Result, error) {
	var backend registryv1alpha1.RegistryBackend
	err := r.Get(ctx, client.ObjectKey{
		Namespace: cr.Spec.BackendRef.Namespace,
		Name:      cr.Spec.BackendRef.Name,
	}, &backend)
	if err != nil {
		if apierrors.IsNotFound(err) {
			res, _ := r.provisioning(ctx, cr,
				fmt.Sprintf("Backend %s/%s not found; waiting", cr.Spec.BackendRef.Namespace, cr.Spec.BackendRef.Name),
				30*time.Second)
			return nil, res, nil
		}
		res, e := r.transient(ctx, cr, "get Backend", err)
		return nil, res, e
	}
	if backend.Status.Phase != phaseReady || backend.Status.AdminSecretName == "" || backend.Status.RegistryURL == "" {
		res, _ := r.provisioning(ctx, cr,
			fmt.Sprintf("Backend %s phase=%s; waiting for Ready", backend.Name, backend.Status.Phase),
			15*time.Second)
		return nil, res, nil
	}
	return &backend, ctrl.Result{}, nil
}

// ensureCredentials mints a project robot account only if the credentials
// Secret does not already exist, then writes an owned Secret. Re-minting would
// invalidate the user's stored credentials, so it is done exactly once.
func (r *RegistryInstanceReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.RegistryInstance, cli *harbor.Client, projectName, registryURL, credName string) error {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: credName}
	var existing corev1.Secret
	if err := r.Get(ctx, key, &existing); err == nil {
		return nil // already provisioned
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	robotName := "ci-" + shortSuffix(cr)
	robot, err := cli.CreateProjectRobotAccount(ctx, projectName, robotName)
	if err != nil {
		return fmt.Errorf("create robot account: %w", err)
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credName,
			Namespace: cr.Namespace,
			Labels:    propagatedLabels(cr),
		},
		Type: corev1.SecretTypeOpaque,
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

// handleDelete runs the instance finalizer. With reclaimPolicy=Delete it also
// removes the Harbor project upstream (best-effort). The credentials Secret is
// GC'd via its owner reference.
func (r *RegistryInstanceReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.RegistryInstance, log logr.Logger) (ctrl.Result, error) {
	//Here finalizer not added but delete set means already deleted CR but reconciled for one last time.
	if !controllerutil.ContainsFinalizer(cr, instanceFinalizer) {
		return ctrl.Result{}, nil
	}

	//If reclaim policy is Delete, then remove the project from the backend.
	if cr.Spec.ReclaimPolicy == reclaimDelete {
		if err := r.cleanupUpstream(ctx, cr, log); err != nil {
			// If the Backend is simply gone, don't trap the CR forever.
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{RequeueAfter: 15 * time.Second}, err
			}
		}
	}

	controllerutil.RemoveFinalizer(cr, instanceFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// cleanupUpstream deletes the Harbor project backing this instance.
func (r *RegistryInstanceReconciler) cleanupUpstream(ctx context.Context, cr *registryv1alpha1.RegistryInstance, log logr.Logger) error {
	var backend registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: cr.Spec.BackendRef.Namespace,
		Name:      cr.Spec.BackendRef.Name,
	}, &backend); err != nil {
		return err // NotFound → caller skips
	}
	if backend.Status.Phase != phaseReady || backend.Status.AdminSecretName == "" {
		return nil // nothing reachable to clean up
	}
	adminPass, err := r.readSecretKey(ctx, harborNamespace(backend.Spec.TenantID), backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	cli := r.harborClient(backend.Status.RegistryURL, adminPass)
	projectName := projectNameFor(cr)
	log.Info("deleting Harbor project", "project", projectName)
	if err := cli.DeleteProject(ctx, projectName); err != nil {
		return fmt.Errorf("delete Harbor project: %w", err)
	}
	return nil
}

// readSecretKey returns one key's value from a Secret.
func (r *RegistryInstanceReconciler) readSecretKey(ctx context.Context, namespace, name, key string) (string, error) {
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
func (r *RegistryInstanceReconciler) harborClient(url, adminPass string) *harbor.Client {
	if r.HelmCfg.InsecureHarborTLS {
		return harbor.NewInsecureClient(url, adminPass)
	}
	return harbor.NewClient(url, adminPass)
}

// --- naming helpers ---

// projectNameFor returns the Harbor project name: RegistryName if set,
// otherwise ProjectID.
func projectNameFor(cr *registryv1alpha1.RegistryInstance) string {
	if cr.Spec.RegistryName != "" {
		return cr.Spec.RegistryName
	}
	return cr.Spec.ProjectID
}

// projectQuotaGi maps a plan to its Harbor project storage quota, in
// gibibytes — separate from the Backend's whole-deployment TenantPlan sizes.
var projectQuotaGi = map[string]int64{
	"starter":      5,
	"professional": 20,
	"enterprise":   100,
}

// projectQuotaBytes resolves a plan to a Harbor project storage quota in
// bytes. Plan-name validation delegates to helm.PlanFor, the single source
// of truth for valid plan names.
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

// credentialsSecretName returns the name of the robot credentials Secret.
func credentialsSecretName(cr *registryv1alpha1.RegistryInstance) string {
	return "registry-credentials-" + cr.Name
}

// shortSuffix returns the CR name lowercased and truncated to 12 characters.
func shortSuffix(cr *registryv1alpha1.RegistryInstance) string {
	s := cr.Name
	if len(s) > 12 {
		s = s[:12]
	}
	return strings.ToLower(s)
}

// propagatedLabels copies dc-api.wso2.com/* labels from the CR onto the
// credentials Secret (platform contract).
func propagatedLabels(cr *registryv1alpha1.RegistryInstance) map[string]string {
	out := map[string]string{}
	for k, v := range cr.Labels {
		if strings.HasPrefix(k, "dc-api.wso2.com/") {
			out[k] = v
		}
	}
	return out
}

// --- status helpers ---

// patchStatus applies mutate to the latest status, retrying once on conflict.
func (r *RegistryInstanceReconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryInstanceStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.RegistryInstance
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
func (r *RegistryInstanceReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.RegistryInstance, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

// transient records a retryable failure and returns the error for backoff.
func (r *RegistryInstanceReconciler) transient(ctx context.Context, cr *registryv1alpha1.RegistryInstance, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonTransient, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonTransient, msg)
	})
	return ctrl.Result{}, cause
}

// fail marks a spec error that retrying cannot resolve: sets Failed and stops
// requeueing. Only for causes traceable to the spec, since editing it
// re-triggers reconcile through the watch.
func (r *RegistryInstanceReconciler) fail(ctx context.Context, cr *registryv1alpha1.RegistryInstance, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Event(cr, corev1.EventTypeWarning, reasonError, msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phaseFailed
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonError, msg)
	})
	return ctrl.Result{}, reconcile.TerminalError(cause)
}

// SetupWithManager registers the controller with the manager.
func (r *RegistryInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryInstance{}).
		Owns(&corev1.Secret{}).
		Named("registryinstance").
		Complete(r)
}
