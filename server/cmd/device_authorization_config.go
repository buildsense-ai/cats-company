package main

import (
	"fmt"
	"strings"
)

func useRedisDeviceAuthorization(cfg DeviceAuthorizationConfig) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.Store), "redis")
}

func validateDeviceAuthorizationConfig(runtime RuntimeConfig, cfg DeviceAuthorizationConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Store))
	if mode != "" && mode != "memory" && mode != "redis" {
		return fmt.Errorf("unsupported device authorization store (expected memory or redis)")
	}
	configured := mode != "" || strings.TrimSpace(cfg.RedisURL) != "" || strings.TrimSpace(cfg.RedisKeyPrefix) != "" || cfg.RequireDurable
	if configured && useRedisRuntime(runtime) {
		return fmt.Errorf("choose either shared Redis runtime or independent device authorization storage, not both")
	}
	if cfg.RequireDurable && !useRedisDeviceAuthorization(cfg) {
		return fmt.Errorf("durable device authorization required: explicitly configure device authorization Redis")
	}
	if useRedisDeviceAuthorization(cfg) && strings.TrimSpace(cfg.RedisURL) == "" {
		return fmt.Errorf("device authorization Redis URL is required")
	}
	if !useRedisDeviceAuthorization(cfg) && (strings.TrimSpace(cfg.RedisURL) != "" || strings.TrimSpace(cfg.RedisKeyPrefix) != "") {
		return fmt.Errorf("device authorization Redis configuration requires explicit redis store")
	}
	return nil
}
