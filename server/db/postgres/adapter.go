// Package postgres implements PostgreSQL database adapter for Cats Company.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/openchat/openchat/server/store"
)

// PoolConfig holds connection pool configuration.
type PoolConfig struct {
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
}

// DefaultPoolConfig returns sensible defaults for connection pool.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    128,
		MaxIdleConns:    32,
		ConnMaxLifetime: 10 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
}

// Adapter is the PostgreSQL database adapter.
type Adapter struct {
	db         *sql.DB
	dsn        string
	poolConfig PoolConfig
}

var _ store.Store = (*Adapter)(nil)
var _ store.ConversationTaskStatusStore = (*Adapter)(nil)
var _ store.ConversationTaskStatusRecoveryStore = (*Adapter)(nil)
var _ store.ConversationTaskGenerationStore = (*Adapter)(nil)
var _ store.ProjectStore = (*Adapter)(nil)
var _ store.ProjectTopicStore = (*Adapter)(nil)
var _ store.PushSubscriptionStore = (*Adapter)(nil)
var _ store.ConversationNotificationPreferenceStore = (*Adapter)(nil)
var _ store.BotSkillMutationPolicyStore = (*Adapter)(nil)
var _ store.BotSkillMutationStore = (*Adapter)(nil)

// Open initializes the database connection with default pool settings.
func (a *Adapter) Open(dsn string) error {
	return a.OpenWithConfig(dsn, DefaultPoolConfig())
}

// OpenWithConfig initializes the database connection with custom pool settings.
func (a *Adapter) OpenWithConfig(dsn string, pool PoolConfig) error {
	var err error
	a.dsn = dsn
	a.poolConfig = pool
	a.db, err = sql.Open("pgx", dsn)
	if err != nil {
		return err
	}

	a.db.SetMaxOpenConns(pool.MaxOpenConns)
	a.db.SetMaxIdleConns(pool.MaxIdleConns)
	a.db.SetConnMaxLifetime(pool.ConnMaxLifetime)
	if pool.ConnMaxIdleTime > 0 {
		a.db.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
	}

	return a.db.Ping()
}

// Close shuts down the database connection.
func (a *Adapter) Close() error {
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

// IsConnected checks if the database connection is still alive.
func (a *Adapter) IsConnected() bool {
	if a.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return a.db.PingContext(ctx) == nil
}

// HealthCheck returns detailed health status for monitoring.
func (a *Adapter) HealthCheck() map[string]interface{} {
	connected := a.IsConnected()
	result := map[string]interface{}{
		"connected": connected,
		"status":    "healthy",
	}

	if connected && a.db != nil {
		stats := a.db.Stats()
		result["pool"] = map[string]interface{}{
			"open_connections": stats.OpenConnections,
			"in_use":           stats.InUse,
			"idle":             stats.Idle,
			"max_open":         stats.MaxOpenConnections,
			"wait_count":       stats.WaitCount,
		}
		if stats.WaitCount > 1000 {
			result["status"] = "warning"
			result["message"] = fmt.Sprintf("high wait count: %d", stats.WaitCount)
		}
		if stats.OpenConnections == stats.MaxOpenConnections && stats.MaxOpenConnections > 0 {
			result["status"] = "warning"
			result["message"] = "connection pool at capacity"
		}
	} else {
		result["status"] = "unhealthy"
		result["message"] = "database not connected"
	}

	return result
}

func inPlaceholders(start, count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for i := 0; i < count; i++ {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ",")
}
