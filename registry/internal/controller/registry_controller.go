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

// RegistryReconciler serves one Registry: a project inside its namespace's
// Harbor, with credentials written to a Secret beside the Registry.
//
// The backend is the one in the Registry's own namespace, found by fixed name
// rather than referenced, so a Registry can only ever reach that Harbor. The
// namespace's first Registry causes it to be created; the rest reuse it and
// only add a project inside it.
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

	// 1. Bind to the namespace's Harbor, creating it if this is the namespace's
	// first Registry, and wait until it serves API requests.
	backend, res, err := r.bindBackend(ctx, &cr)
	if backend == nil {
		return res, err
	}

	// 2. Read Harbor's admin password from the backend's Secret. Backend,
	// Secret, Harbor's pods, and this Registry all share one namespace.
	adminPass, err := r.readSecretKey(ctx, backend.Namespace, backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
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
	// Harbor's own built-in project is a name a user could plausibly pick, and
	// adopting it would be silent (see reservedProjectNames). Terminal, not
	// transient: only renaming the Registry can resolve it.
	if reservedProjectNames[projectName] {
		return r.fail(ctx, &cr, "resolve Harbor project",
			fmt.Errorf("%q is a Harbor built-in project name; rename this Registry", projectName))
	}
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

// bindBackend resolves the Harbor serving this Registry's namespace, creating
// it if the namespace has none yet, and reports whether it is ready to accept
// projects. Returns (nil, result, err) when the caller should stop this pass.
//
// Nothing outside this reconciler has to exist first — no separate
// provisioning step, no pre-created object, not even a namespace, since the
// Registry being reconciled is already proof its namespace exists.
func (r *RegistryReconciler) bindBackend(ctx context.Context, cr *registryv1alpha1.Registry) (*registryv1alpha1.RegistryBackend, ctrl.Result, error) {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: backendName}
	var backend registryv1alpha1.RegistryBackend
	err := r.Get(ctx, key, &backend)

	switch {
	case apierrors.IsNotFound(err):
		// First Registry in this namespace: provision the Harbor deployment.
		// Every Registry here targets the same fixed name, so concurrent first
		// Registries attempt the identical object and the API server settles
		// the race.
		if cerr := r.Create(ctx, defaultBackend(cr.Namespace)); cerr != nil {
			if !apierrors.IsAlreadyExists(cerr) {
				return nil, ctrl.Result{}, fmt.Errorf("create RegistryBackend %s: %w", key, cerr)
			}
			// Another Registry created it first; use theirs.
		} else {
			r.Recorder.Eventf(cr, corev1.EventTypeNormal, reasonProvisioning,
				"provisioning Harbor for namespace %s", cr.Namespace)
		}
		res, _ := r.provisioning(ctx, cr,
			fmt.Sprintf("provisioning Harbor for namespace %s; this takes a few minutes", cr.Namespace),
			15*time.Second)
		return nil, res, nil

	case err != nil:
		res, e := r.transient(ctx, cr, "get RegistryBackend", err)
		return nil, res, e
	}

	if backend.Status.Phase != phaseReady ||
		backend.Status.AdminSecretName == "" ||
		backend.Status.RegistryURL == "" {
		res, _ := r.provisioning(ctx, cr,
			fmt.Sprintf("Harbor for namespace %s is %s; waiting", cr.Namespace, phaseOrPending(backend.Status.Phase)),
			15*time.Second)
		return nil, res, nil
	}
	return &backend, ctrl.Result{}, nil
}

// defaultBackend builds the Harbor deployment created for a namespace's first
// Registry. It starts at the smallest plan and grows as that namespace's
// registries commit storage.
func defaultBackend(namespace string) *registryv1alpha1.RegistryBackend {
	return &registryv1alpha1.RegistryBackend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backendName,
			Namespace: namespace,
		},
		Spec: registryv1alpha1.RegistryBackendSpec{
			Plan: planOrder[0],
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
// Secret does not already exist, then writes an owned Secret. The Secret's
// presence is what makes this once-only: re-minting would invalidate
// credentials already in use. When it is absent, any robot from a previous
// half-finished attempt is unusable — its secret was never stored — so
// EnsureProjectRobotAccount replaces it rather than failing against it.
func (r *RegistryReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client, projectName, registryURL, credName string) error {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: credName}
	var existing corev1.Secret
	if err := r.Get(ctx, key, &existing); err == nil {
		return nil // already provisioned
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	robot, err := cli.EnsureProjectRobotAccount(ctx, projectName, robotAccountName(cr))
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

// handleDelete runs the Registry finalizer: the Harbor project and every image
// in it are removed. There is no retain option — deleting a Registry means
// deleting the registry.
//
// The credentials Secret is garbage-collected through its owner reference.
func (r *RegistryReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) (ctrl.Result, error) {
	// No finalizer means cleanup already finished and this is the last pass.
	if !controllerutil.ContainsFinalizer(cr, registryFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.deleteHarborProject(ctx, cr, log); err != nil {
		// Keep the finalizer until the project is really gone, so a Harbor that
		// is merely unreachable cannot leave images behind that the user asked
		// to destroy. Only a missing backend counts as nothing-to-do.
		if !apierrors.IsNotFound(err) {
			r.Recorder.Event(cr, corev1.EventTypeWarning, reasonTransient,
				"waiting to delete Harbor project: "+err.Error())
			return ctrl.Result{RequeueAfter: 15 * time.Second}, err
		}
	}

	controllerutil.RemoveFinalizer(cr, registryFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteHarborProject removes the Harbor project backing this Registry, along
// with every repository inside it.
//
// status.harborProject is the gate: it is written only once the project really
// exists in Harbor, so an empty value means nothing was created and there is
// nothing to reclaim.
//
// Two cases short-circuit deliberately. A backend that is absent, or itself
// being deleted, means the whole Harbor is going away — including its volumes —
// so deleting individual projects first is wasted work against an API that is
// about to disappear, and would only slow a cascade down or wedge it if Harbor
// were already unreachable.
func (r *RegistryReconciler) deleteHarborProject(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) error {
	projectName := cr.Status.HarborProject
	if projectName == "" {
		return nil
	}

	var backend registryv1alpha1.RegistryBackend
	if err := r.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: backendName}, &backend); err != nil {
		return err // NotFound → caller treats as nothing to clean up
	}
	if !backend.DeletionTimestamp.IsZero() {
		log.Info("backend is being deleted; skipping per-project cleanup", "project", projectName)
		return nil
	}
	if backend.Status.AdminSecretName == "" {
		// Harbor was never provisioned far enough to hold a project.
		return nil
	}

	adminPass, err := r.readSecretKey(ctx, backend.Namespace, backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return err
	}
	cli := r.harborClient(backend.Status.RegistryURL, adminPass)

	// Harbor refuses to delete a project that still holds repositories (412),
	// so empty it first. Without this the finalizer retries that 412 forever and
	// the Registry never leaves Terminating.
	repos, err := cli.ListRepositories(ctx, projectName)
	if err != nil {
		return fmt.Errorf("list repositories in %s: %w", projectName, err)
	}
	for _, repo := range repos {
		log.Info("deleting Harbor repository", "project", projectName, "repository", repo)
		if err := cli.DeleteRepository(ctx, projectName, repo); err != nil {
			return fmt.Errorf("delete repository %s/%s: %w", projectName, repo, err)
		}
	}

	log.Info("deleting Harbor project", "project", projectName, "repositories", len(repos))
	if err := cli.DeleteProject(ctx, projectName); err != nil {
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

// harborClient returns a Harbor client for the backend at url.
func (r *RegistryReconciler) harborClient(url, adminPass string) *harbor.Client {
	return harbor.NewClient(url, adminPass)
}

// --- naming ---

// reservedProjectNames are Harbor project names a Registry must never resolve
// to. Harbor's built-in "library" project is PUBLIC, and CreateHarborProject
// treats 409 as success so creation is idempotent — so a Registry named
// "library" would bind straight to it, mint a push robot against it, and
// report Ready while publishing the namespace's images world-readable.
var reservedProjectNames = map[string]bool{"library": true}

// harborProjectName returns the Harbor project for a Registry: its own name.
//
// Each Harbor serves exactly one namespace, and Kubernetes forbids two objects
// of a kind sharing a name in one namespace, so the Registry's name is already
// collision-free — and a DNS label, which always satisfies Harbor's project
// naming rules. Uniqueness matters: Harbor answers 409 for an existing project
// and the operator treats that as success, so a collision would silently hand
// one Registry a robot account on another's images.
func harborProjectName(cr *registryv1alpha1.Registry) string {
	return strings.ToLower(cr.Name)
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

// registriesForBackend maps a backend to the Registries it serves: every
// Registry in its namespace, bound or not. The unbound ones are included
// deliberately — the namespace's first Registry is exactly the one waiting on
// this Harbor to come up.
func (r *RegistryReconciler) registriesForBackend(ctx context.Context, obj client.Object) []reconcile.Request {
	backend, ok := obj.(*registryv1alpha1.RegistryBackend)
	if !ok {
		return nil
	}
	var list registryv1alpha1.RegistryList
	if err := r.List(ctx, &list, client.InNamespace(backend.Namespace)); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return out
}
