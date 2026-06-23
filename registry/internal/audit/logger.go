package audit

import (
	"context"

	"go.uber.org/zap"

	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/db"
)

type Logger struct {
	store  *db.Store
	logger *zap.Logger
}

func NewLogger(store *db.Store, logger *zap.Logger) *Logger {
	return &Logger{store: store, logger: logger}
}

type Event struct {
	TenantID   string
	Action     string // REGISTRY_CREATE | REGISTRY_DELETE | GET_CREDENTIALS | ROTATE_CREDENTIALS
	ActorID    string
	ActorEmail string
	SourceIP   string
	Result     string // SUCCESS | FAILURE
	Details    map[string]interface{}
}

func (l *Logger) Log(ctx context.Context, e Event) {
	entry := &db.AuditEntry{
		TenantID:   e.TenantID,
		Action:     e.Action,
		ActorID:    e.ActorID,
		ActorEmail: e.ActorEmail,
		SourceIP:   e.SourceIP,
		Result:     e.Result,
		Details:    e.Details,
	}

	if err := l.store.WriteAuditLog(ctx, entry); err != nil {
		// Audit failures must not block the main path — log and continue
		l.logger.Error("failed to write audit log",
			zap.Error(err),
			zap.String("action", e.Action),
			zap.String("tenant", e.TenantID),
		)
	}

	l.logger.Info("audit",
		zap.String("action", e.Action),
		zap.String("tenant", e.TenantID),
		zap.String("actor", e.ActorEmail),
		zap.String("result", e.Result),
		zap.String("ip", e.SourceIP),
	)
}
