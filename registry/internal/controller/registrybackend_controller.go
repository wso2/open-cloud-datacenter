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

// RegistryBackendReconciler provisions one Harbor per namespace. The CR is the
// source of truth; Harbor is installed via Helm from inside the reconcile loop
// and its readiness is polled with RequeueAfter.
//
// Everything it touches lives in cr.Namespace: Harbor's pods and volumes, the
// credential Secret they mount, and the Registries it serves. The operator
// never creates that namespace — it is the namespace a user already had.
type RegistryBackendReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Helm     HarborDeployer
	HelmCfg  config.HelmConfig
}

// HarborDeployer is the Helm surface this reconciler drives. Narrowing it to the
// two calls actually made keeps the release mechanics substitutable, so the
// delete path can be exercised without a cluster to install into.
type HarborDeployer interface {
	Install(ctx context.Context, namespace string, values []byte) error
	Uninstall(ctx context.Context, namespace string) error
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends;registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/status;registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registrybackends/finalizers;registries/finalizers,verbs=update
//
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//
// Harbor's chart objects are created by Helm using this pod's ServiceAccount,
// so the ClusterRole must grant the resource types the Harbor chart manages:
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;serviceaccounts;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete

// Reconcile converges one namespace's Harbor deployment: credentials, Helm
// release, PVC sizes, then readiness.
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

	// Size from the effective plan, which is the greater of the administrator's
	// floor and what autoscaling has already grown to. Growth is recorded in
	// status rather than written back into the spec.
	plan := largerPlan(cr.Spec.Plan, cr.Status.EffectivePlan)
	if plan == "" {
		plan = planOrder[0]
	}

	// Harbor deploys into the backend's own namespace — the namespace a user
	// already had and put a Registry in. Nothing has to be created, labeled, or
	// looked up first, and the credential Secret lands where Harbor's pods can
	// mount it (a pod can only read Secrets from its own namespace).
	harborNS := cr.Namespace

	// 1. Ensure the full pinned Harbor credential set exists in an operator-owned
	// Secret (admin + db passwords and the chart's internal keys — pinned so that
	// re-rendered values are deterministic and upgrades never rotate secrets).
	secrets, secretName, err := r.ensureAdminSecret(ctx, &cr)
	if err != nil {
		return r.transient(ctx, &cr, "", "ensure admin secret", err)
	}

	// 2. Render values and converge the Helm release (install when missing,
	// upgrade when the desired values drift from the deployed ones — e.g. a
	// plan change; no-op otherwise).
	tplan, err := helm.PlanFor(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	values, err := helm.GenerateValues(helm.ValuesInput{
		Namespace:     harborNS,
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
		// Rendering only fails on a template defect, which no spec edit can
		// resolve — keep retrying so the error stays visible in logs and
		// metrics rather than going silent after one report.
		return r.transient(ctx, &cr, secretName, "generate values", err)
	}
	if err := r.Helm.Install(ctx, harborNS, values); err != nil {
		return r.transient(ctx, &cr, secretName, "helm install/upgrade", err)
	}

	// 2b. Grow plan-controlled PVCs to the plan's sizes (helm cannot resize
	// existing claims; PVC expansion is a direct, grow-only patch).
	if err := r.ensureStorageSize(ctx, &cr, tplan); err != nil {
		return r.transient(ctx, &cr, secretName, "ensure storage size", err)
	}

	// 3. Wait for Harbor to accept API requests (poll, don't block).
	registryURL := fmt.Sprintf("https://registry.%s.%s", harborNS, r.HelmCfg.BaseDomain)
	cli := r.harborClient(registryURL, secrets.AdminPass)
	if err := cli.Ping(ctx); err != nil {
		// Carry the real cause into the message. This is an expected wait, so it
		// is not logged as an error — which makes status.message the ONLY place
		// the reason ever surfaces. Dropping err here turns "DNS failed", "no
		// route to the ingress", and "Harbor answered 503" into one
		// indistinguishable string.
		return r.provisioning(ctx, &cr, secretName, registryURL,
			fmt.Sprintf("waiting for Harbor to accept API requests at %s: %v", registryURL, err),
			15*time.Second)
	}

	// 4. Apply Harbor system configuration (idempotent).
	if err := cli.Configure(ctx); err != nil {
		return r.provisioning(ctx, &cr, secretName, registryURL,
			fmt.Sprintf("configuring Harbor: %v", err), 15*time.Second)
	}

	// 4b. Keep garbage collection scheduled. Deleting a Registry removes its
	// project's manifests but leaves the blobs on the shared registry volume,
	// and those orphans count against no project's quota — so they are invisible
	// to the capacity measurement in step 5 while still consuming disk. GC is
	// what bounds that term. Runs an hour after the scan sweep so the two do not
	// contend.
	if err := cli.EnsureGCSchedule(ctx, gcCron); err != nil {
		return r.provisioning(ctx, &cr, secretName, registryURL,
			fmt.Sprintf("scheduling Harbor garbage collection: %v", err), 15*time.Second)
	}

	// 4c. Keep the vulnerability sweep scheduled. A scan records what was known
	// when it ran, so without a repeating pass a CVE published after an image is
	// pushed never appears against that image.
	if err := cli.EnsureScanAllSchedule(ctx, scanAllCron); err != nil {
		return r.provisioning(ctx, &cr, secretName, registryURL,
			fmt.Sprintf("scheduling Harbor vulnerability scan: %v", err), 15*time.Second)
	}

	// 5. Measure what the namespace's registries have committed, and decide whether
	// the deployment needs to be larger. Harbor reports both the quota it has
	// promised and what is consumed, so no metrics pipeline is involved.
	totals, terr := cli.ProjectStorageTotals(ctx)
	if terr != nil {
		// Capacity planning is not worth failing a healthy Harbor over; report
		// Ready and retry the measurement on the next pass.
		log.Info("could not read Harbor storage totals; skipping this pass", "error", terr)
	}
	nextPlan := plan
	if terr == nil {
		if p, perr := computeEffectivePlan(&cr, totals); perr != nil {
			// Not a spec error: cr.Spec.Plan is CRD-enum-validated and the loop
			// in computeEffectivePlan only ever advances through planOrder, so
			// this can only fire if planOrder (autoscale.go) and the plans map
			// (values_generator.go) have drifted apart in a code change — a bug
			// in this operator's own binary, identical for every backend, not
			// something a spec edit on this object could ever fix. fail() would
			// go quiet after one report; keep this loud and retried instead.
			return r.transient(ctx, &cr, secretName, "compute effective plan", perr)
		} else {
			nextPlan = p
		}
	}
	registryCount, cerr := r.countRegistries(ctx, &cr)
	if cerr != nil {
		return r.transient(ctx, &cr, secretName, "count registries", cerr)
	}

	// 6. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryBackendStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.RegistryURL = registryURL
		s.AdminSecretName = secretName
		s.EffectivePlan = nextPlan
		s.RegistryCount = registryCount
		if terr == nil {
			s.CommittedStorageBytes = totals.Committed
			s.UsedStorageBytes = totals.Used
			s.UnlimitedProjectCount = int32(totals.Unlimited)
		}
		s.Message = "Harbor is running"
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "Harbor is running")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Event(&cr, corev1.EventTypeNormal, reasonReady, "Harbor ready at "+registryURL)

	if nextPlan != plan {
		// Requeue immediately: the next pass renders and expands at the larger
		// plan, keeping growth in one place rather than duplicating it here.
		r.Recorder.Eventf(&cr, corev1.EventTypeNormal, reasonResized,
			"growing Harbor from %s to %s; registries have committed %d bytes", plan, nextPlan, totals.Committed)
		return ctrl.Result{Requeue: true}, nil
	}

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
		// The Secret is meaningless once the volumes its encryption key unlocks
		// are gone, so it is bound to the backend's lifetime. The finalizer still
		// deletes it explicitly; this guarantees reclaim even if that never runs.
		if oerr := controllerutil.SetControllerReference(cr, &sec, r.Scheme); oerr != nil {
			return harborSecrets{}, "", oerr
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
func (r *RegistryBackendReconciler) ensureStorageSize(ctx context.Context, cr *registryv1alpha1.RegistryBackend, plan helm.SizePlan) error {
	// Selected by the Helm release label, not just by namespace: Harbor shares
	// this namespace with the user's own workloads, and the name matching below
	// would otherwise resize any unrelated claim ending in "-registry".
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs,
		client.InNamespace(cr.Namespace),
		harborReleaseSelector(cr.Namespace),
	); err != nil {
		return fmt.Errorf("list PVCs: %w", err)
	}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		// Within one Harbor release the name suffixes are unambiguous: the
		// registry image store and the database.
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

// dependentRegistries returns the Registries this backend serves: every
// Registry in its namespace. A Registry cannot name a backend or reach one
// outside its namespace, so membership needs no status matching — which also
// means a Registry counts as a dependent from the instant it is created,
// before it has ever reconciled.
func (r *RegistryBackendReconciler) dependentRegistries(ctx context.Context, cr *registryv1alpha1.RegistryBackend) ([]string, error) {
	var list registryv1alpha1.RegistryList
	if err := r.List(ctx, &list, client.InNamespace(cr.Namespace)); err != nil {
		return nil, fmt.Errorf("list Registries: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return names, nil
}

// countRegistries reports how many Registries this backend serves.
func (r *RegistryBackendReconciler) countRegistries(ctx context.Context, cr *registryv1alpha1.RegistryBackend) (int32, error) {
	names, err := r.dependentRegistries(ctx, cr)
	if err != nil {
		return 0, err
	}
	return int32(len(names)), nil
}

// handleDelete runs the Backend finalizer: refuse while Registries still depend
// on this Harbor, then uninstall it and reclaim its volumes and credentials.
//
// Deleting a backend always destroys its data — there is no retain option. What
// protects images is the dependent guard, not a policy field: while a Registry
// exists the delete is refused, and overriding that takes the force annotation.
func (r *RegistryBackendReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.RegistryBackend, log logr.Logger) (ctrl.Result, error) {
	//Check if finalizer added to the cr. If not do nothing. Which means CR is reconciling for one last time
	if !controllerutil.ContainsFinalizer(cr, backendFinalizer) {
		return ctrl.Result{}, nil
	}

	// Dependent guard. Refusing by default is what stops a bulk image delete
	// from being a side effect of removing the CR that merely describes the
	// deployment; the force annotation is the deliberate override.
	dependents, err := r.dependentRegistries(ctx, cr)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(dependents) > 0 {
		var msg string
		requeue := 15 * time.Second

		if cr.Annotations[forceAnnotation] == "true" {
			// Cascade. Deleting the Registries is not optional politeness —
			// leaving them behind would be a bug, because each one's reconcile
			// calls bindBackend, finds no backend, and creates a new one, so the
			// Harbor would come straight back. Their own finalizers do the
			// releasing, and they skip per-project Harbor cleanup because this
			// backend is already terminating.
			log.Info("force delete: cascading to registries", "count", len(dependents), "registries", dependents)
			for _, name := range dependents {
				reg := registryv1alpha1.Registry{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cr.Namespace}}
				if derr := r.Delete(ctx, &reg); derr != nil && !apierrors.IsNotFound(derr) {
					return ctrl.Result{}, fmt.Errorf("delete Registry %s: %w", name, derr)
				}
			}
			msg = fmt.Sprintf("force delete: removing %d Registry(s) and all their images: %v", len(dependents), dependents)
			// Short requeue: the Registries must actually be gone before Harbor
			// is torn down, so this pass ends and the next one re-checks.
			requeue = 5 * time.Second
		} else {
			msg = fmt.Sprintf("blocked by %d Registry(s): %v — delete them first, or annotate %s=true",
				len(dependents), dependents, forceAnnotation)
		}

		r.Recorder.Event(cr, corev1.EventTypeWarning, reasonBlocked, msg)
		_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryBackendStatus) {
			s.Phase = phaseTerminating
			s.Message = msg
			setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonBlocked, msg)
		})
		return ctrl.Result{RequeueAfter: requeue}, nil
	}
	// Uninstall Harbor. Uninstall tolerates a release that is not there, so
	// this is also the correct path for a backend that never got far enough to
	// install one.
	if err := r.Helm.Uninstall(ctx, cr.Namespace); err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, fmt.Errorf("helm uninstall: %w", err)
	}

	// Reclaim everything. Helm keeps PVCs (resourcePolicy: keep), so they are
	// deleted explicitly, and the credential Secret goes with them — it is
	// meaningless once the volumes its encryption key unlocks are gone.
	if err := r.deletePVCs(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete pvcs: %w", err)
	}
	sec := corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      cr.Name + "-harbor-admin",
		Namespace: cr.Namespace,
	}}
	if err := r.Delete(ctx, &sec); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("delete admin secret: %w", err)
	}
	log.Info("deleted Harbor PVCs and credentials", "namespace", cr.Namespace)

	controllerutil.RemoveFinalizer(cr, backendFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deletePVCs removes the Harbor release's PVCs (best-effort, by the Helm
// instance label — never by namespace alone, which the user's own workloads
// share).
func (r *RegistryBackendReconciler) deletePVCs(ctx context.Context, cr *registryv1alpha1.RegistryBackend) error {
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs,
		client.InNamespace(cr.Namespace),
		harborReleaseSelector(cr.Namespace),
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

// Harbor's chart labels its objects with the LEGACY Helm convention —
// `release`/`app`/`component`/`heritage` — not `app.kubernetes.io/instance`.
// Selecting on the modern key matches nothing, silently turning both PVC paths
// into no-ops: storage would never grow, and reclaimPolicy: Delete would leave
// every volume behind.
const (
	helmReleaseLabel = "release"
	harborAppLabel   = "app"
	harborAppValue   = "harbor"
)

// harborReleaseSelector matches exactly the objects this namespace's Harbor
// release owns. Harbor shares the namespace with the user's own workloads, so
// every PVC lookup must go through this and never through namespace alone.
func harborReleaseSelector(namespace string) client.MatchingLabels {
	return client.MatchingLabels{
		harborAppLabel:   harborAppValue,
		helmReleaseLabel: helm.ReleaseName(namespace),
	}
}

// harborClient returns a Harbor client for the backend at url.
func (r *RegistryBackendReconciler) harborClient(url, adminPass string) *harbor.Client {
	return harbor.NewClient(url, adminPass)
}

// --- status helpers ---

// patchStatus applies mutate to the latest status, retrying once on conflict.
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

// fail marks a spec error that retrying cannot resolve: sets Failed and stops
// requeueing. Only for causes traceable to the spec, since editing it
// re-triggers reconcile through the watch.
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

// SetupWithManager registers the controller with the manager. No
// Owns(&corev1.Secret{}): this reconciler leaves the credential Secret
// unowned (see ensureAdminSecret), so an ownership watch would match nothing.
func (r *RegistryBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.RegistryBackend{}).
		Named("registrybackend").
		Complete(r)
}
