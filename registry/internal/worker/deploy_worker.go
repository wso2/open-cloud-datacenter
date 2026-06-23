package worker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"go.uber.org/zap"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/audit"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/crypto"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/helm"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/k8s"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/metrics"
)

// Worker polls for PENDING deployments and drives them to READY or FAILED.
type Worker struct {
	store    *db.Store
	helm     *helm.Deployer
	k8s      *k8s.Client
	cipher   *crypto.Cipher
	helmCfg  config.HelmConfig
	audit    *audit.Logger
	metrics  *metrics.Registry
	logger   *zap.Logger
}

func New(
	store *db.Store,
	helm *helm.Deployer,
	k8s *k8s.Client,
	cipher *crypto.Cipher,
	helmCfg config.HelmConfig,
	auditLog *audit.Logger,
	reg *metrics.Registry,
	logger *zap.Logger,
) *Worker {
	return &Worker{
		store: store, helm: helm, k8s: k8s,
		cipher: cipher, helmCfg: helmCfg, audit: auditLog,
		metrics: reg, logger: logger,
	}
}

// Run is the main loop — picks up PENDING Harbor deployments every 5s,
// DELETING deployments every 10s, and PENDING Harbor projects every 5s.
func (w *Worker) Run(ctx context.Context) {
	deployTicker  := time.NewTicker(5 * time.Second)
	deleteTicker  := time.NewTicker(10 * time.Second)
	projectTicker := time.NewTicker(5 * time.Second)
	defer deployTicker.Stop()
	defer deleteTicker.Stop()
	defer projectTicker.Stop()
	w.logger.Info("deploy worker started")
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("deploy worker stopped")
			return
		case <-deployTicker.C:
			w.processPending(ctx)
		case <-deleteTicker.C:
			w.processDeleting(ctx)
		case <-projectTicker.C:
			w.processPendingProjects(ctx)
		}
	}
}

func (w *Worker) processPending(ctx context.Context) {
	deployment, err := w.store.GetOldestPending(ctx)
	if err != nil || deployment == nil {
		return
	}
	w.logger.Info("picked up pending deployment", zap.String("tenant", deployment.TenantID))
	go w.deploy(context.Background(), deployment)
}

func (w *Worker) processDeleting(ctx context.Context) {
	deployment, err := w.store.GetOldestDeleting(ctx)
	if err != nil || deployment == nil {
		return
	}
	w.logger.Info("picked up deleting deployment",
		zap.String("tenant", deployment.TenantID),
		zap.Bool("hard_delete", deployment.HardDelete),
	)
	go w.deleteHarbor(context.Background(), deployment)
}

// deleteHarbor drives a single tenant from DELETING → DELETED (or releases lock on failure).
func (w *Worker) deleteHarbor(ctx context.Context, d *db.RegistryDeployment) {
	tenantID := d.TenantID
	namespace := d.Namespace
	mode := "soft"
	if d.HardDelete {
		mode = "hard"
	}

	timer := w.metrics.StartDeleteTimer(mode)
	defer timer.ObserveDuration()

	log := w.logger.With(
		zap.String("tenant", tenantID),
		zap.String("mode", mode),
	)

	fail := func(step string, err error) {
		log.Error("delete failed",
			zap.String("step", step),
			zap.Error(err),
		)
		w.metrics.DeleteResult("failure", mode)
		w.audit.Log(ctx, audit.Event{
			TenantID: tenantID,
			Action:   "REGISTRY_DELETE_FAILED",
			Result:   "FAILURE",
			Details:  map[string]interface{}{"step": step, "error": err.Error()},
		})
		// Release lock so another worker replica or the next tick can retry.
		w.store.ReleaseDeleteLock(context.Background(), tenantID)
	}

	deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Step 1: Helm uninstall — removes Harbor pods and services.
	// IgnoreNotFound=true (already set in Uninstall) makes this idempotent.
	log.Info("uninstalling harbor helm release")
	if err := w.helm.Uninstall(deleteCtx, tenantID, namespace); err != nil {
		fail("helm_uninstall", err)
		return
	}

	if d.HardDelete {
		// Step 2 (hard only): Delete all PVCs — destroys all image data permanently.
		log.Info("deleting harbor PVCs")
		if err := w.k8s.DeletePVCs(deleteCtx, namespace); err != nil {
			fail("delete_pvcs", err)
			return
		}

		// Step 3 (hard only): Delete the entire namespace.
		log.Info("deleting harbor namespace")
		if err := w.k8s.DeleteNamespace(deleteCtx, namespace); err != nil {
			fail("delete_namespace", err)
			return
		}

		// Step 4 (hard only): Delete the K8s credentials Secret so dc-api can no longer
		// read credentials for this tenant via its Harvester dynamic client.
		log.Info("deleting credentials secret")
		if err := w.k8s.DeleteSecret(deleteCtx, "registry", credentialSecretName(tenantID)); err != nil {
			fail("delete_credentials_secret", err)
			return
		}

		// Step 5 (hard only): Remove all DB records for this tenant permanently.
		if err := w.store.HardDeleteTenant(context.Background(), tenantID); err != nil {
			fail("hard_delete_db", err)
			return
		}
	} else {
		// Soft delete: mark as DELETED, keep PVCs and DB row so data can be restored.
		if err := w.store.SetDeploymentDeleted(context.Background(), tenantID); err != nil {
			fail("set_deleted", err)
			return
		}
	}

	w.metrics.DeleteResult("success", mode)
	log.Info("harbor deletion complete", zap.String("tenant", tenantID))

	w.audit.Log(ctx, audit.Event{
		TenantID: tenantID,
		Action:   "REGISTRY_DELETE_COMPLETE",
		Result:   "SUCCESS",
		Details:  map[string]interface{}{"mode": mode, "namespace": namespace},
	})
}

// deploy drives a single tenant from PENDING → READY (or FAILED).
func (w *Worker) deploy(ctx context.Context, d *db.RegistryDeployment) {
	timer := w.metrics.StartProvisionTimer(d.TenantID)
	defer timer.ObserveDuration()

	tenantID := d.TenantID
	namespace := d.Namespace

	fail := func(step string, err error) {
		msg := fmt.Sprintf("step %s failed: %v", step, err)
		w.logger.Error("provision failed", zap.String("tenant", tenantID), zap.String("step", step), zap.Error(err))
		w.store.UpdateDeploymentStatus(ctx, tenantID, db.StatusFailed, msg)
		w.metrics.ProvisionResult(tenantID, "failure")
	}

	// --- Step 1: Namespace ---
	w.setProgress(ctx, tenantID, "namespace", "STARTING")
	if err := w.k8s.EnsureNamespace(ctx, namespace, tenantID); err != nil {
		fail("namespace", err)
		return
	}
	if err := w.k8s.ApplyNetworkPolicy(ctx, namespace, w.helmCfg.IngressControllerNamespace); err != nil {
		fail("network_policy", err)
		return
	}
	w.setProgress(ctx, tenantID, "namespace", "READY")

	// --- Step 2: Generate Helm values ---
	plan, err := helm.PlanFor(d.Plan)
	if err != nil {
		fail("plan_lookup", err)
		return
	}
	adminPass, err := crypto.GeneratePassword(32)
	if err != nil {
		fail("generate_admin_pass", err)
		return
	}
	dbPass, err := crypto.GeneratePassword(32)
	if err != nil {
		fail("generate_db_pass", err)
		return
	}

	values, err := helm.GenerateValues(helm.ValuesInput{
		TenantID:     tenantID,
		AdminPass:    adminPass,
		DBPass:       dbPass,
		BaseDomain:   w.helmCfg.BaseDomain,
		StorageClass: w.helmCfg.StorageClass,
		IngressClass: w.helmCfg.IngressClass,
		CertIssuer:   w.helmCfg.CertIssuer,
		Plan:         plan,
	})
	if err != nil {
		fail("generate_values", err)
		return
	}

	// --- Step 3: Helm install ---
	w.store.UpdateDeploymentStatus(ctx, tenantID, db.StatusDeploying, "")
	w.setProgress(ctx, tenantID, "helm_install", "STARTING")

	if err := w.helm.Install(ctx, tenantID, namespace, values); err != nil {
		fail("helm_install", err)
		return
	}
	w.setProgress(ctx, tenantID, "helm_install", "READY")

	// --- Step 4: Wait for pods ---
	w.logger.Info("waiting for harbor pods", zap.String("tenant", tenantID))
	podCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	err = w.k8s.WaitForAllReady(podCtx, namespace, func(status map[string]bool) {
		progress := make(map[string]string)
		for comp, ready := range status {
			if ready {
				progress[comp] = "READY"
			} else {
				progress[comp] = "STARTING"
			}
		}
		w.store.UpdateProgress(ctx, tenantID, progress)
	})
	if err != nil {
		fail("pods_ready", fmt.Errorf("pods did not become ready: %w", err))
		return
	}

	// --- Step 5: Harbor API bootstrap ---
	w.setProgress(ctx, tenantID, "bootstrap", "STARTING")
	registryURL := fmt.Sprintf("https://registry.%s.%s", tenantID, w.helmCfg.BaseDomain)

	var harborClient *harbor.Client
	if os.Getenv("HARBOR_INSECURE_BOOTSTRAP") == "true" {
		harborClient = harbor.NewInsecureClient(registryURL, adminPass)
	} else {
		harborClient = harbor.NewClient(registryURL, adminPass)
	}

	// Give Harbor's API a moment after pods ready
	bootstrapCtx, cancel2 := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel2()
	if err := harborClient.WaitReady(bootstrapCtx); err != nil {
		fail("harbor_api_ready", err)
		return
	}

	robot, err := harborClient.Bootstrap(ctx)
	if err != nil {
		fail("harbor_bootstrap", err)
		return
	}
	w.setProgress(ctx, tenantID, "bootstrap", "READY")

	// --- Step 6: Encrypt and store credentials ---
	encToken, tokenNonce, err := w.cipher.EncryptString(robot.Secret)
	if err != nil {
		fail("encrypt_robot_token", err)
		return
	}
	encAdmin, adminNonce, err := w.cipher.EncryptString(adminPass)
	if err != nil {
		fail("encrypt_admin_pw", err)
		return
	}

	if err := w.store.SaveCredentials(ctx, &db.RegistryCredentials{
		TenantID:         tenantID,
		RobotUsername:    robot.Name,
		EncryptedToken:   encToken,
		TokenNonce:       tokenNonce,
		AdminUsername:    "admin",
		EncryptedAdminPW: encAdmin,
		AdminPWNonce:     adminNonce,
	}); err != nil {
		fail("save_credentials", err)
		return
	}

	// Write plaintext credentials to a K8s Secret in the registry namespace.
	// dc-api reads this Secret via its Harvester dynamic client — it never calls
	// our HTTP gateway for credentials (same pattern as dbaas operator).
	loginServer := strings.TrimPrefix(registryURL, "https://")
	if err := w.k8s.ApplySecret(ctx, buildCredentialSecret(tenantID, robot.Name, robot.Secret, adminPass, registryURL, loginServer)); err != nil {
		fail("write_credentials_secret", err)
		return
	}

	// --- Step 7: Mark READY ---
	if err := w.store.SetDeploymentReady(ctx, tenantID, registryURL); err != nil {
		fail("set_ready", err)
		return
	}

	w.metrics.ProvisionResult(tenantID, "success")
	w.logger.Info("harbor deployment complete",
		zap.String("tenant", tenantID),
		zap.String("url", registryURL),
	)
}

func (w *Worker) setProgress(ctx context.Context, tenantID, component, status string) {
	// Read current progress, update one key, write back
	deployment, err := w.store.GetDeployment(ctx, tenantID)
	if err != nil || deployment == nil {
		return
	}
	progress := deployment.Progress
	if progress == nil {
		progress = map[string]string{}
	}
	progress[component] = status
	w.store.UpdateProgress(ctx, tenantID, progress)
}

// buildCredentialSecret constructs the K8s Secret that dc-api reads via its
// Harvester dynamic client to get robot token + admin password without calling
// the provisioner HTTP gateway (same pattern as the dbaas operator).
func buildCredentialSecret(tenantID, robotUsername, robotPassword, adminPassword, registryURL, loginServer string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-credentials-" + tenantID,
			Namespace: "registry",
			Labels: map[string]string{
				"lkdc.wso2.com/tenant":    tenantID,
				"lkdc.wso2.com/component": "registry-credentials",
			},
		},
		Data: map[string][]byte{
			"robot_username": []byte(robotUsername),
			"robot_password": []byte(robotPassword),
			"admin_password": []byte(adminPassword),
			"registry_url":   []byte(registryURL),
			"login_server":   []byte(loginServer),
		},
	}
}

// credentialSecretName returns the name of the K8s Secret holding a tenant's
// registry credentials (used by both the deploy worker and the delete worker).
func credentialSecretName(tenantID string) string {
	return "registry-credentials-" + tenantID
}

// ── Phase 2: Harbor project creation ─────────────────────────────────────────

func (w *Worker) processPendingProjects(ctx context.Context) {
	proj, err := w.store.GetOldestPendingProject(ctx)
	if err != nil || proj == nil {
		return
	}
	w.logger.Info("picked up pending harbor project",
		zap.String("tenant", proj.TenantID),
		zap.String("project", proj.ProjectID))
	go w.createHarborProject(context.Background(), proj)
}

// createHarborProject drives a single registry instance from PENDING → READY
// inside an already-running Harbor instance.
func (w *Worker) createHarborProject(ctx context.Context, proj *db.RegistryProject) {
	tenantID          := proj.TenantID
	projectID         := proj.ProjectID
	registryName      := proj.RegistryName
	harborProjectName := proj.HarborProjectName

	fail := func(err error) {
		w.logger.Error("harbor project creation failed",
			zap.String("tenant", tenantID),
			zap.String("project", projectID),
			zap.String("registry", registryName),
			zap.Error(err))
		w.store.SetProjectFailed(context.Background(), tenantID, projectID, registryName, err.Error())
	}

	// Retrieve the admin password stored during Phase 1 bootstrap.
	creds, err := w.store.GetCredentials(ctx, tenantID)
	if err != nil || creds == nil {
		fail(fmt.Errorf("no harbor admin credentials for tenant %s", tenantID))
		return
	}
	adminPass, err := w.cipher.DecryptString(creds.EncryptedAdminPW, creds.AdminPWNonce)
	if err != nil {
		fail(fmt.Errorf("decrypt admin password: %w", err))
		return
	}

	registryURL := fmt.Sprintf("https://registry.%s.%s", tenantID, w.helmCfg.BaseDomain)

	var harborClient *harbor.Client
	if os.Getenv("HARBOR_INSECURE_BOOTSTRAP") == "true" {
		harborClient = harbor.NewInsecureClient(registryURL, adminPass)
	} else {
		harborClient = harbor.NewClient(registryURL, adminPass)
	}

	// Step 1: Create Harbor project (idempotent — 409 = already exists).
	if err := harborClient.CreateHarborProject(ctx, harborProjectName); err != nil {
		fail(fmt.Errorf("create harbor project %q: %w", harborProjectName, err))
		return
	}

	// Step 2: Create project-scoped robot account.
	robotName := "ci-" + projectID
	robot, err := harborClient.CreateProjectRobotAccount(ctx, harborProjectName, robotName)
	if err != nil {
		fail(fmt.Errorf("create project robot %q: %w", robotName, err))
		return
	}

	// Step 3: Encrypt and save project credentials.
	encToken, tokenNonce, err := w.cipher.EncryptString(robot.Secret)
	if err != nil {
		fail(fmt.Errorf("encrypt robot token: %w", err))
		return
	}
	if err := w.store.SaveProjectCredentials(ctx, &db.RegistryProjectCredentials{
		TenantID:       tenantID,
		ProjectID:      projectID,
		RegistryName:   registryName,
		RobotUsername:  robot.Name,
		EncryptedToken: encToken,
		TokenNonce:     tokenNonce,
	}); err != nil {
		fail(fmt.Errorf("save project credentials: %w", err))
		return
	}

	// Step 4: Write per-registry K8s Secret into the project namespace.
	// dc-api reads it from dc-<tenantID>-<projectID> — matching the RegistryInstance CR namespace.
	loginServer := strings.TrimPrefix(registryURL, "https://")
	projectNS := "dc-" + tenantID + "-" + projectID

	// Include the Harbor ingress CA so dc-api can hand it to clients alongside the
	// robot creds (same pattern as the dbaas operator's ca_cert). Best-effort —
	// an empty CA just means the client must trust the cert another way.
	caCert, caErr := w.k8s.GetSecret(ctx, tenantID+"-management", tenantID+"-harbor-tls", "ca.crt")
	if caErr != nil {
		w.logger.Warn("could not read harbor CA cert; credentials secret will omit ca_cert",
			zap.String("tenant", tenantID), zap.Error(caErr))
	}

	if err := w.k8s.ApplySecret(ctx, buildProjectCredentialSecret(
		projectNS, registryName, robot.Name, robot.Secret, registryURL, loginServer, harborProjectName, caCert,
	)); err != nil {
		fail(fmt.Errorf("write project credentials secret: %w", err))
		return
	}

	// Step 5: Mark READY.
	if err := w.store.SetProjectReady(ctx, tenantID, projectID, registryName, robot.ID); err != nil {
		fail(fmt.Errorf("set project ready: %w", err))
		return
	}

	w.logger.Info("harbor project ready",
		zap.String("tenant", tenantID),
		zap.String("project", projectID),
		zap.String("registry", registryName),
		zap.String("harbor_project", harborProjectName))
}

// buildProjectCredentialSecret builds the K8s Secret dc-api reads for per-project credentials.
// namespace is "dc-<tenantID>-<projectID>" (the RegistryInstance CR's namespace).
// crName is the RegistryInstance CR name (e.g. "reg-5f3a8c1d").
func buildProjectCredentialSecret(namespace, crName, robotUser, robotPass, registryURL, loginServer, harborProject, caCert string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registry-credentials-" + crName,
			Namespace: namespace,
			Labels: map[string]string{
				"lkdc.wso2.com/cr-name":   crName,
				"lkdc.wso2.com/component": "registry-project-credentials",
			},
		},
		Data: map[string][]byte{
			"robot_username": []byte(robotUser),
			"robot_password": []byte(robotPass),
			"registry_url":   []byte(registryURL),
			"login_server":   []byte(loginServer),
			"harbor_project": []byte(harborProject),
			"ca_cert":        []byte(caCert),
		},
	}
}
