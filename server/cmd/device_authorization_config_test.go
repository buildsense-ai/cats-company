package main

import "testing"

func TestDeviceAuthorizationIndependentConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime RuntimeConfig
		auth    DeviceAuthorizationConfig
		valid   bool
	}{
		{"legacy memory", RuntimeConfig{}, DeviceAuthorizationConfig{}, true},
		{"legacy shared Redis", RuntimeConfig{Store: "redis", RedisURL: "redis://shared"}, DeviceAuthorizationConfig{}, true},
		{"narrow Redis", RuntimeConfig{Store: "memory"}, DeviceAuthorizationConfig{Store: "redis", RedisURL: "redis://auth", RequireDurable: true}, true},
		{"required but missing", RuntimeConfig{}, DeviceAuthorizationConfig{RequireDurable: true}, false},
		{"no implicit Redis", RuntimeConfig{}, DeviceAuthorizationConfig{RedisURL: "redis://auth"}, false},
		{"missing URL", RuntimeConfig{}, DeviceAuthorizationConfig{Store: "redis"}, false},
		{"unknown mode", RuntimeConfig{}, DeviceAuthorizationConfig{Store: "redsi"}, false},
		{"conflicting shared Redis", RuntimeConfig{RedisURL: "redis://shared"}, DeviceAuthorizationConfig{Store: "redis", RedisURL: "redis://auth"}, false},
		{"shared cannot override auth to memory", RuntimeConfig{Store: "redis"}, DeviceAuthorizationConfig{Store: "memory"}, false},
		{"ignored URL forbidden", RuntimeConfig{Store: "memory"}, DeviceAuthorizationConfig{Store: "memory", RedisURL: "redis://auth"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateDeviceAuthorizationConfig(tc.runtime, tc.auth); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestDeviceAuthorizationEnvironmentNeverEnablesSharedRuntime(t *testing.T) {
	for _, raw := range []string{"true", "1", "typo", "false", "0"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OC_DEVICE_AUTH_STORE", "redis")
			t.Setenv("OC_DEVICE_AUTH_REDIS_URL", "redis://auth")
			t.Setenv("OC_DEVICE_AUTH_REDIS_KEY_PREFIX", "test:auth")
			t.Setenv("OC_DEVICE_AUTH_REQUIRE_DURABLE", raw)
			t.Setenv("OC_RUNTIME_STORE", "memory")
			cfg := &Config{}
			applyEnvOverrides(cfg)
			if useRedisRuntime(cfg.Runtime) || !useRedisDeviceAuthorization(cfg.DeviceAuthorization) || cfg.Runtime.RedisURL != "" {
				t.Fatal("authorization config enabled shared Redis runtime")
			}
			if cfg.DeviceAuthorization.RequireDurable != (raw != "false" && raw != "0") || cfg.DeviceAuthorization.RedisKeyPrefix != "test:auth" {
				t.Fatal("independent environment ignored")
			}
		})
	}
}
