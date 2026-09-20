// Package postgres implements PostgreSQL database adapter for Cats Company.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/openchat/openchat/server/db/poolhealth"
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
	db          *sql.DB
	dsn         string
	poolConfig  PoolConfig
	healthWatch poolhealth.Tracker
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
var _ store.AgentArtifactTagStore = (*Adapter)(nil)
var _ store.ImageUpscaleTaskStore = (*Adapter)(nil)

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
		pool := map[string]interface{}{
			"open_connections": stats.OpenConnections,
			"in_use":           stats.InUse,
			"idle":             stats.Idle,
			"max_open":         stats.MaxOpenConnections,
			// Cumulative for the lifetime of the process; kept for operators but
			// no longer used to gate readiness on its own.
			"wait_count": stats.WaitCount,
		}
		result["pool"] = pool

		// All connections checked out right now. OpenConnections == max_open with
		// idle connections is normal pool behaviour and must not raise a warning.
		if stats.MaxOpenConnections > 0 && stats.InUse >= stats.MaxOpenConnections {
			result["status"] = "warning"
			result["message"] = "connection pool saturated"
		}

		// Waits accumulated since the previous recorded sample. The cumulative
		// WaitCount only ever grows, so a fixed threshold on it would leave a
		// long-running process stuck in "warning" until the next restart.
		if delta, elapsed, ok := a.healthWatch.Observe(stats.WaitCount, time.Now()); ok {
			pool["waits_since_last_sample"] = delta
			pool["sample_window_seconds"] = int(elapsed.Seconds())
			if delta >= poolhealth.BurstThreshold {
				result["status"] = "warning"
				result["message"] = fmt.Sprintf("recent pool queueing: %d waits in %s", delta, elapsed.Round(time.Second))
			}
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
