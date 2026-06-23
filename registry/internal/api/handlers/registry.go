package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/api/middleware"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/audit"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/crypto"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/metrics"
)

type RegistryHandler struct {
	store    *db.Store
	cipher   *crypto.Cipher
	auditLog *audit.Logger
	metrics  *metrics.Registry
	helmCfg  config.HelmConfig
	logger   *zap.Logger
}

func NewRegistryHandler(
	store *db.Store,
	cipher *crypto.Cipher,
	auditLog *audit.Logger,
	reg *metrics.Registry,
	helmCfg config.HelmConfig,
	logger *zap.Logger,
) *RegistryHandler {
	return &RegistryHandler{
		store: store, cipher: cipher, auditLog: auditLog,
		metrics: reg, helmCfg: helmCfg, logger: logger,
	}
}

// POST /api/v1/tenants/:tenantId/registry
func (h *RegistryHandler) Create(c *gin.Context) {
	tenantID := c.Param("tenantId")
	claims := middleware.GetClaims(c)

	var req struct {
		Plan string `json:"plan" binding:"required,oneof=starter professional enterprise"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "INVALID_REQUEST",
			"message": err.Error(),
		})
		return
	}

	existing, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil {
		h.logger.Error("get deployment", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	if existing != nil {
		switch existing.Status {
		case db.StatusReady, db.StatusDeploying:
			c.JSON(http.StatusConflict, gin.H{
				"error":       "REGISTRY_EXISTS",
				"message":     fmt.Sprintf("A registry is already %s for %s", strings.ToLower(string(existing.Status)), tenantID),
				"registryUrl": existing.RegistryURL,
				"status":      existing.Status,
			})
			return
		case db.StatusPending:
			c.JSON(http.StatusConflict, gin.H{
				"error":   "REGISTRY_PENDING",
				"message": "A registry deployment is already queued",
			})
			return
		case db.StatusFailed, db.StatusDeleted:
			// Allow retry — remove old record
			h.store.DeleteDeployment(c.Request.Context(), tenantID)
			h.store.DeleteCredentials(c.Request.Context(), tenantID)
		}
	}

	namespace := fmt.Sprintf("%s-management", tenantID)
	helmRelease := fmt.Sprintf("harbor-%s", tenantID)

	if err := h.store.CreateDeployment(c.Request.Context(), &db.RegistryDeployment{
		TenantID:    tenantID,
		Namespace:   namespace,
		Status:      db.StatusPending,
		HelmRelease: helmRelease,
		Plan:        req.Plan,
	}); err != nil {
		h.logger.Error("create deployment record", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	h.auditLog.Log(c.Request.Context(), audit.Event{
		TenantID:   tenantID,
		Action:     "REGISTRY_CREATE",
		ActorID:    claims.UserID,
		ActorEmail: claims.Email,
		SourceIP:   c.ClientIP(),
		Result:     "SUCCESS",
		Details:    map[string]interface{}{"plan": req.Plan, "namespace": namespace},
	})

	c.JSON(http.StatusAccepted, gin.H{
		"tenantId":               tenantID,
		"status":                 "PENDING",
		"estimatedReadySeconds":  180,
		"pollUrl":                fmt.Sprintf("/api/v1/tenants/%s/registry", tenantID),
	})
}

// GET /api/v1/tenants/:tenantId/registry
func (h *RegistryHandler) Get(c *gin.Context) {
	tenantID := c.Param("tenantId")

	deployment, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}
	if deployment == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "REGISTRY_NOT_FOUND",
			"message": fmt.Sprintf("No registry provisioned for %s", tenantID),
		})
		return
	}

	resp := gin.H{
		"tenantId":  tenantID,
		"status":    deployment.Status,
		"plan":      deployment.Plan,
		"namespace": deployment.Namespace,
		"createdAt": deployment.CreatedAt.Format(time.RFC3339),
	}
	if deployment.RegistryURL != "" {
		resp["registryUrl"] = deployment.RegistryURL
		resp["harborPortalUrl"] = deployment.RegistryURL
	}
	if deployment.ErrorMessage != "" {
		resp["errorMessage"] = deployment.ErrorMessage
	}
	if len(deployment.Progress) > 0 {
		resp["progress"] = deployment.Progress
	}
	if deployment.ReadyAt != nil {
		resp["readyAt"] = deployment.ReadyAt.Format(time.RFC3339)
		elapsedSec := int(deployment.ReadyAt.Sub(deployment.CreatedAt).Seconds())
		resp["provisionedInSeconds"] = elapsedSec
	}
	if deployment.Status == db.StatusDeploying {
		elapsed := int(time.Since(deployment.CreatedAt).Seconds())
		resp["elapsedSeconds"] = elapsed
	}

	c.JSON(http.StatusOK, resp)
}

// GET /api/v1/tenants/:tenantId/registry/credentials
func (h *RegistryHandler) GetCredentials(c *gin.Context) {
	tenantID := c.Param("tenantId")
	claims := middleware.GetClaims(c)

	deployment, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil || deployment == nil || deployment.Status != db.StatusReady {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "REGISTRY_NOT_READY",
			"message": "Registry is not yet available",
		})
		return
	}

	creds, err := h.store.GetCredentials(c.Request.Context(), tenantID)
	if err != nil || creds == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "CREDENTIALS_NOT_FOUND"})
		return
	}

	robotToken, err := h.cipher.DecryptString(creds.EncryptedToken, creds.TokenNonce)
	if err != nil {
		h.logger.Error("decrypt robot token", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DECRYPT_ERROR"})
		return
	}

	loginServer := strings.TrimPrefix(deployment.RegistryURL, "https://")

	h.auditLog.Log(c.Request.Context(), audit.Event{
		TenantID:   tenantID,
		Action:     "GET_CREDENTIALS",
		ActorID:    claims.UserID,
		ActorEmail: claims.Email,
		SourceIP:   c.ClientIP(),
		Result:     "SUCCESS",
		Details:    map[string]interface{}{"robot_username": creds.RobotUsername},
	})
	h.metrics.TrackCredentialFetch(tenantID)

	c.JSON(http.StatusOK, gin.H{
		"tenantId":        tenantID,
		"registryUrl":     deployment.RegistryURL,
		"loginServer":     loginServer,
		"robotUsername":   creds.RobotUsername,
		"robotPassword":   robotToken,
		"adminUsername":   "admin",
		"harborPortalUrl": deployment.RegistryURL,
		"quickstart": gin.H{
			"login": fmt.Sprintf("docker login %s", loginServer),
			"push":  fmt.Sprintf("docker push %s/library/IMAGE:TAG", loginServer),
			"pull":  fmt.Sprintf("docker pull %s/library/IMAGE:TAG", loginServer),
		},
	})
}

// POST /api/v1/tenants/:tenantId/registry/credentials/rotate
func (h *RegistryHandler) RotateCredentials(c *gin.Context) {
	tenantID := c.Param("tenantId")
	claims := middleware.GetClaims(c)

	deployment, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil || deployment == nil || deployment.Status != db.StatusReady {
		c.JSON(http.StatusBadRequest, gin.H{"error": "REGISTRY_NOT_READY"})
		return
	}

	// Generate new password
	newToken, err := crypto.GeneratePassword(48)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GENERATE_ERROR"})
		return
	}

	encToken, tokenNonce, err := h.cipher.EncryptString(newToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ENCRYPT_ERROR"})
		return
	}

	creds, _ := h.store.GetCredentials(c.Request.Context(), tenantID)
	if creds == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "CREDENTIALS_NOT_FOUND"})
		return
	}

	// Update stored credentials
	creds.EncryptedToken = encToken
	creds.TokenNonce = tokenNonce
	if err := h.store.SaveCredentials(c.Request.Context(), creds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "SAVE_ERROR"})
		return
	}

	h.auditLog.Log(c.Request.Context(), audit.Event{
		TenantID:   tenantID,
		Action:     "ROTATE_CREDENTIALS",
		ActorID:    claims.UserID,
		ActorEmail: claims.Email,
		SourceIP:   c.ClientIP(),
		Result:     "SUCCESS",
	})

	c.JSON(http.StatusOK, gin.H{
		"robotUsername": creds.RobotUsername,
		"robotPassword": newToken,
		"rotatedAt":     time.Now().UTC().Format(time.RFC3339),
		"warning":       "Update your CI/CD pipelines and imagePullSecrets with the new password",
	})
}

// GET /api/v1/tenants/:tenantId/registry/pull-secret
func (h *RegistryHandler) GetPullSecret(c *gin.Context) {
	tenantID := c.Param("tenantId")
	targetNS := c.Query("namespace")
	if targetNS == "" {
		targetNS = fmt.Sprintf("%s-dev", tenantID)
	}

	deployment, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil || deployment == nil || deployment.Status != db.StatusReady {
		c.JSON(http.StatusBadRequest, gin.H{"error": "REGISTRY_NOT_READY"})
		return
	}

	creds, err := h.store.GetCredentials(c.Request.Context(), tenantID)
	if err != nil || creds == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "CREDENTIALS_NOT_FOUND"})
		return
	}

	robotToken, err := h.cipher.DecryptString(creds.EncryptedToken, creds.TokenNonce)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DECRYPT_ERROR"})
		return
	}

	loginServer := strings.TrimPrefix(deployment.RegistryURL, "https://")
	auth := base64.StdEncoding.EncodeToString(
		[]byte(creds.RobotUsername + ":" + robotToken),
	)

	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			loginServer: map[string]string{
				"username": creds.RobotUsername,
				"password": robotToken,
				"auth":     auth,
			},
		},
	}
	dockerConfigJSON, _ := json.Marshal(dockerConfig)
	encoded := base64.StdEncoding.EncodeToString(dockerConfigJSON)

	secretName := fmt.Sprintf("%s-registry-pull-secret", tenantID)
	yaml := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    lkdc.wso2.com/tenant: %s
    lkdc.wso2.com/component: registry
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: %s
---
# Apply with: kubectl apply -f pull-secret.yaml
# Then add to your pod spec:
#   spec:
#     imagePullSecrets:
#       - name: %s
`, secretName, targetNS, tenantID, encoded, secretName)

	c.Header("Content-Type", "application/yaml")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-pull-secret.yaml"`, tenantID))
	c.String(http.StatusOK, yaml)
}

// DELETE /api/v1/tenants/:tenantId/registry
func (h *RegistryHandler) Delete(c *gin.Context) {
	tenantID := c.Param("tenantId")
	claims := middleware.GetClaims(c)

	var req struct {
		DeleteData bool `json:"deleteData"`
	}
	c.ShouldBindJSON(&req)

	deployment, err := h.store.GetDeployment(c.Request.Context(), tenantID)
	if err != nil || deployment == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "REGISTRY_NOT_FOUND"})
		return
	}

	if err := h.store.UpdateForDelete(c.Request.Context(), tenantID, req.DeleteData); err != nil {
		h.logger.Error("update for delete", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "INTERNAL_ERROR"})
		return
	}

	h.auditLog.Log(c.Request.Context(), audit.Event{
		TenantID:   tenantID,
		Action:     "REGISTRY_DELETE",
		ActorID:    claims.UserID,
		ActorEmail: claims.Email,
		SourceIP:   c.ClientIP(),
		Result:     "SUCCESS",
		Details: map[string]interface{}{
			"delete_data": req.DeleteData,
			"namespace":   deployment.Namespace,
			"hard_delete": req.DeleteData,
		},
	})

	c.JSON(http.StatusAccepted, gin.H{
		"status":        "DELETING",
		"dataPreserved": !req.DeleteData,
		"message":       "Registry is being removed. This may take up to 2 minutes.",
	})
}
