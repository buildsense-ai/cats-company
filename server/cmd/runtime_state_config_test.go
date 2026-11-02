package main

import "testing"

func TestRuntimeDurabilityConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   RuntimeConfig
		valid bool
	}{
		{"legacy memory", RuntimeConfig{}, true},
		{"required missing", RuntimeConfig{RequireDurable: true}, false},
		{"explicit memory", RuntimeConfig{Store: "memory", RedisURL: "redis://localhost", RequireDurable: true}, false},
		{"redis missing url", RuntimeConfig{Store: "redis"}, false},
		{"unknown store", RuntimeConfig{Store: "redsi"}, false},
		{"redis", RuntimeConfig{Store: "redis", RedisURL: "redis://localhost", RequireDurable: true}, true},
		{"implicit redis", RuntimeConfig{RedisURL: "redis://localhost", RequireDurable: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRuntimeConfig(tc.cfg); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestRuntimeDurabilityEnvironment(t *testing.T) {
	for _, raw := range []string{"true", "1", "typo", "false", "0"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("OC_RUNTIME_REQUIRE_DURABLE", raw)
			cfg := &Config{}
			applyEnvOverrides(cfg)
			want := raw != "false" && raw != "0"
			if cfg.Runtime.RequireDurable != want {
				t.Fatalf("durability gate = %v, want %v", cfg.Runtime.RequireDurable, want)
			}
		})
	}
}
