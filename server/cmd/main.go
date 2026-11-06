// Package main is the entry point for the Cats Company server.
package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
)

// Config holds the server configuration.
type Config struct {
	Listen              string                    `json:"listen"`
	GRPCPort            string                    `json:"grpc_port"`
	Database            DBConfig                  `json:"database"`
	WebSocket           WSConfig                  `json:"websocket"`
	Static              StaticConfig              `json:"static"`
	Runtime             RuntimeConfig             `json:"runtime"`
	DeviceAuthorization DeviceAuthorizationConfig `json:"device_authorization"`
}

type DBConfig struct {
	Driver          string `json:"driver"`
	DSN             string `json:"dsn"`
	MaxOpenConns    int    `json:"max_open_conns"`
	MaxIdleConns    int    `json:"max_idle_conns"`
	ConnMaxLifetime string `json:"conn_max_lifetime"`
	ConnMaxIdleTime string `json:"conn_max_idle_time"`
}

type WSConfig struct {
	Path string `json:"path"`
}

type StaticConfig struct {
	Dir string `json:"dir"`
}

type RuntimeConfig struct {
	Store          string `json:"store"`
	RedisURL       string `json:"redis_url"`
	RedisKeyPrefix string `json:"redis_key_prefix"`
	RequireDurable bool   `json:"require_durable"`
}

// Independent of Runtime: selecting Redis here must not select Redis routing,
// body leases, attention, pairing or pending RPC storage.
type DeviceAuthorizationConfig struct {
	Store          string `json:"store"`
	RedisURL       string `json:"redis_url"`
	RedisKeyPrefix string `json:"redis_key_prefix"`
	RequireDurable bool   `json:"require_durable"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{}
	if err := json.NewDecoder(f).Decode(cfg); err != nil {
		return nil, err
	}
	applyEnvOverrides(cfg)
	return cfg, nil
}

func defaultConfig() *Config {
	cfg := &Config{
		Listen:   ":6061",
		GRPCPort: ":6062",
		Database: DBConfig{
			Driver: "mysql",
			DSN:    "openchat:openchat@tcp(localhost:3306)/openchat?parseTime=true&charset=utf8mb4",
		},
		WebSocket: WSConfig{Path: "/v0/channels"},
		Static:    StaticConfig{Dir: "../webapp/build"},
	}
	applyEnvOverrides(cfg)
	return cfg
}

func applyEnvOverrides(cfg *Config) {
	if driver := os.Getenv("OC_DB_DRIVER"); driver != "" {
		cfg.Database.Driver = driver
	}
	if dsn := os.Getenv("OC_DB_DSN"); dsn != "" {
		cfg.Database.DSN = dsn
	}
	applyEnvInt("OC_DB_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns)
	applyEnvInt("OC_DB_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns)
	applyEnvString("OC_DB_CONN_MAX_LIFETIME", &cfg.Database.ConnMaxLifetime)
	applyEnvString("OC_DB_CONN_MAX_IDLE_TIME", &cfg.Database.ConnMaxIdleTime)
	applyEnvString("OC_RUNTIME_STORE", &cfg.Runtime.Store)
	applyEnvString("OC_REDIS_URL", &cfg.Runtime.RedisURL)
	applyEnvString("OC_REDIS_KEY_PREFIX", &cfg.Runtime.RedisKeyPrefix)
	if raw := strings.TrimSpace(os.Getenv("OC_RUNTIME_REQUIRE_DURABLE")); raw != "" {
		// Invalid nonempty values must not accidentally disable this safety gate.
		cfg.Runtime.RequireDurable = raw != "0" && !strings.EqualFold(raw, "false")
	}
	applyEnvString("OC_DEVICE_AUTH_STORE", &cfg.DeviceAuthorization.Store)
	applyEnvString("OC_DEVICE_AUTH_REDIS_URL", &cfg.DeviceAuthorization.RedisURL)
	applyEnvString("OC_DEVICE_AUTH_REDIS_KEY_PREFIX", &cfg.DeviceAuthorization.RedisKeyPrefix)
	if raw := strings.TrimSpace(os.Getenv("OC_DEVICE_AUTH_REQUIRE_DURABLE")); raw != "" {
		cfg.DeviceAuthorization.RequireDurable = raw != "0" && !strings.EqualFold(raw, "false")
	}
}

func applyEnvInt(name string, target *int) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		log.Printf("ignoring invalid %s=%q", name, raw)
		return
	}
	*target = value
}

func applyEnvString(name string, target *string) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw != "" {
		*target = raw
	}
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
