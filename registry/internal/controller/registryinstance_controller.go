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

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const instanceFinalizer = "registry.opencloud.wso2.com/instance-cleanup"

// RegistryInstanceReconciler provisions one Harbor project + robot account per
// registry. The robot credentials are written into an owner-referenced K8s
// Secret, which dc-api reads directly.
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
	adminPass, err := r.readSecretKey(ctx, backend.Namespace, backend.Status.AdminSecretName, "HARBOR_ADMIN_PASSWORD")
	if err != nil {
		return r.transient(ctx, &cr, "read backend admin secret", err)
	}
	registryURL := backend.Status.RegistryURL
	cli := r.harborClient(registryURL, adminPass)

	// 3. Create the Harbor project (idempotent — 409 treated as success).
	projectName := projectNameFor(&cr)
	if err := cli.CreateHarborProject(ctx, projectName); err != nil {
		return r.transient(ctx, &cr, "create Harbor project", err)
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
	if !controllerutil.ContainsFinalizer(cr, instanceFinalizer) {
		return ctrl.Result{}, nil
	}

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
	adminPass, err := r.readSecretKey(ctx, backend.Namespace, backend.Status.AdminSecretName, "admin-password")
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

func credentialsSecretName(cr *registryv1alpha1.RegistryInstance) string {
	return "registry-credentials-" + cr.Name
}

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

func (r *RegistryInstanceReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.RegistryInstance, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryInstanceStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

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

func (r *RegistryInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryInstance{}).
		Owns(&corev1.Secret{}).
		Named("registryinstance").
		Complete(r)
}
