package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"go.uber.org/zap"
)

type Claims struct {
	TenantID string
	UserID   string
	Email    string
	Role     string
}

const claimsKey = "claims"

type JWTValidator struct {
	jwksEndpoint string
	issuer       string
	audience     string
	cache        *jwk.Cache
	logger       *zap.Logger
}

func NewJWTValidator(jwksEndpoint, issuer, audience string, logger *zap.Logger) (*JWTValidator, error) {
	cache := jwk.NewCache(context.Background())
	if err := cache.Register(jwksEndpoint, jwk.WithMinRefreshInterval(15*time.Minute)); err != nil {
		return nil, fmt.Errorf("failed to register JWKS endpoint: %w", err)
	}
	// Pre-fetch to fail fast on bad config
	if _, err := cache.Refresh(context.Background(), jwksEndpoint); err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	return &JWTValidator{
		jwksEndpoint: jwksEndpoint,
		issuer:       issuer,
		audience:     audience,
		cache:        cache,
		logger:       logger,
	}, nil
}

func (v *JWTValidator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := extractBearerToken(c.GetHeader("Authorization"))
		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "MISSING_TOKEN",
				"message": "Authorization header with Bearer token is required",
			})
			return
		}

		keySet, err := v.cache.Get(context.Background(), v.jwksEndpoint)
		if err != nil {
			v.logger.Error("failed to get JWKS", zap.Error(err))
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "AUTH_UNAVAILABLE",
				"message": "Authentication service temporarily unavailable",
			})
			return
		}

		token, err := jwt.Parse([]byte(tokenStr),
			jwt.WithKeySet(keySet),
			jwt.WithValidate(true),
			jwt.WithIssuer(v.issuer),
			jwt.WithAudience(v.audience),
		)
		if err != nil {
			v.logger.Debug("token validation failed", zap.Error(err))
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "INVALID_TOKEN",
				"message": "Token is invalid or expired",
			})
			return
		}

		claims := extractClaims(token)

		// Enforce TENANT_ADMIN or PLATFORM_ADMIN
		if claims.Role != "TENANT_ADMIN" && claims.Role != "PLATFORM_ADMIN" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "INSUFFICIENT_ROLE",
				"message": "TENANT_ADMIN or PLATFORM_ADMIN role required",
			})
			return
		}

		c.Set(claimsKey, claims)
		c.Next()
	}
}

// TenantGuard ensures the JWT's tenantId matches the URL param {tenantId}.
// PLATFORM_ADMIN can access any tenant; TENANT_ADMIN can only access their own.
func TenantGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := GetClaims(c)
		if claims == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHENTICATED"})
			return
		}

		urlTenant := c.Param("tenantId")
		if claims.Role != "PLATFORM_ADMIN" && claims.TenantID != urlTenant {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "TENANT_MISMATCH",
				"message": fmt.Sprintf("You do not have access to tenant %s", urlTenant),
			})
			return
		}
		c.Next()
	}
}

func GetClaims(c *gin.Context) *Claims {
	v, exists := c.Get(claimsKey)
	if !exists {
		return nil
	}
	claims, _ := v.(*Claims)
	return claims
}

func extractBearerToken(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// DevAuthBypass injects a PLATFORM_ADMIN claim without validating any token.
// Only used when DEV_SKIP_AUTH=true. Never enable in production.
func DevAuthBypass() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(claimsKey, &Claims{
			TenantID: "dev-tenant",
			UserID:   "dev-user",
			Email:    "dev@localhost",
			Role:     "PLATFORM_ADMIN",
		})
		c.Next()
	}
}

func extractClaims(token jwt.Token) *Claims {
	tenantID, _ := token.Get("tenant_id")
	role, _ := token.Get("role")
	email, _ := token.Get("email")

	tidStr, _ := tenantID.(string)
	roleStr, _ := role.(string)
	emailStr, _ := email.(string)

	return &Claims{
		TenantID: tidStr,
		UserID:   token.Subject(),
		Email:    emailStr,
		Role:     roleStr,
	}
}
