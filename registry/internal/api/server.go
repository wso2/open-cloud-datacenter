package api

import (
	"net/http"
	"os"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/api/handlers"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/api/middleware"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/audit"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/crypto"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/metrics"
)

type Server struct {
	cfg      *config.Config
	store    *db.Store
	cipher   *crypto.Cipher
	auditLog *audit.Logger
	metrics  *metrics.Registry
	logger   *zap.Logger
	router   *gin.Engine
}

func NewServer(
	cfg *config.Config,
	store *db.Store,
	cipher *crypto.Cipher,
	auditLog *audit.Logger,
	reg *metrics.Registry,
	logger *zap.Logger,
) *Server {
	s := &Server{
		cfg: cfg, store: store, cipher: cipher,
		auditLog: auditLog, metrics: reg, logger: logger,
	}
	s.router = s.buildRouter()
	return s
}

func (s *Server) Router() *gin.Engine {
	return s.router
}

func (s *Server) buildRouter() *gin.Engine {
	r := gin.New()

	// Global middleware
	r.Use(middleware.Recovery(s.logger))
	r.Use(middleware.RequestLogger(s.logger))
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"https://*.lkdc.wso2.com", "http://localhost:3000"},
		AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type"},
		ExposeHeaders:    []string{"Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           86400,
	}))

	// Handlers
	h := handlers.NewRegistryHandler(
		s.store, s.cipher, s.auditLog, s.metrics, s.cfg.Helm, s.logger,
	)

	// Health check (no auth)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", func(c *gin.Context) {
		if err := s.store.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus metrics (no auth — typically behind network policy)
	r.GET("/metrics", gin.WrapH(metrics.Handler()))

	// Authenticated API
	api := r.Group("/api/v1")
	api.Use(middleware.ByTenant(1.0/600, 1.0)) // 1/10min create, 1/sec read

	// DEV_SKIP_AUTH=true bypasses JWT validation (dev only — inject platform admin claims)
	if os.Getenv("DEV_SKIP_AUTH") == "true" {
		s.logger.Warn("DEV_SKIP_AUTH is enabled — JWT validation is disabled")
		api.Use(middleware.DevAuthBypass())
	} else {
		jwtValidator, err := middleware.NewJWTValidator(
			s.cfg.Auth.JWKSEndpoint,
			s.cfg.Auth.Issuer,
			s.cfg.Auth.Audience,
			s.logger,
		)
		if err != nil {
			s.logger.Fatal("failed to create JWT validator", zap.Error(err))
		}
		api.Use(jwtValidator.Middleware())
	}

	tenants := api.Group("/tenants/:tenantId")
	tenants.Use(middleware.TenantGuard())
	{
		registry := tenants.Group("/registry")
		registry.POST("", h.Create)
		registry.GET("", h.Get)
		registry.DELETE("", h.Delete)
		registry.GET("/credentials", h.GetCredentials)
		registry.POST("/credentials/rotate", h.RotateCredentials)
		registry.GET("/pull-secret", h.GetPullSecret)
	}

	return r
}
