package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func stubRelayLimits(t *testing.T, status int, payload string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/38/key/limits" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer relay-admin-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestServiceBalanceChecker(t *testing.T) {
	ctx := context.Background()

	t.Run("exhausted", func(t *testing.T) {
		ts := stubRelayLimits(t, 200, `{"uid":38,"configured":true,"limits":{"monthly_budget":{"max_limit":100,"current_usage":100},"model_limits":[]}}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); !errors.Is(err, ErrQuotaExhausted) {
			t.Fatalf("err = %v, want ErrQuotaExhausted", err)
		}
	})

	t.Run("remaining", func(t *testing.T) {
		ts := stubRelayLimits(t, 200, `{"uid":38,"configured":true,"limits":{"monthly_budget":{"max_limit":100,"current_usage":87.5},"model_limits":[]}}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("relay_5xx", func(t *testing.T) {
		ts := stubRelayLimits(t, 500, `{"error":"boom"}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); !errors.Is(err, ErrRelayUnavailable) {
			t.Fatalf("err = %v, want ErrRelayUnavailable", err)
		}
	})

	t.Run("shared_pool_row_exhausted", func(t *testing.T) {
		ts := stubRelayLimits(t, 200, `{"uid":38,"configured":true,"limits":{"monthly_budget":{"max_limit":100,"current_usage":1},"model_limits":[{"provider":"anthropic","model":"m","shared_budget":true,"budget":{"max_limit":50,"current_usage":50}}]}}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); !errors.Is(err, ErrQuotaExhausted) {
			t.Fatalf("err = %v, want ErrQuotaExhausted", err)
		}
	})

	t.Run("model_row_not_shared_skipped", func(t *testing.T) {
		ts := stubRelayLimits(t, 200, `{"uid":38,"configured":true,"limits":{"monthly_budget":{"max_limit":100,"current_usage":1},"model_limits":[{"provider":"anthropic","model":"m","shared_budget":false,"budget":{"max_limit":50,"current_usage":50}}]}}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); err != nil {
			t.Fatalf("err = %v, want nil (per-model budgets are enforced upstream)", err)
		}
	})

	t.Run("unspecified_limits_allowed", func(t *testing.T) {
		ts := stubRelayLimits(t, 200, `{"uid":38,"configured":true,"limits":{"monthly_budget":{"max_limit":0,"current_usage":0},"model_limits":[]}}`)
		t.Setenv("CATS_RELAY_ADMIN_URL", ts.URL)
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "relay-admin-token")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); err != nil {
			t.Fatalf("err = %v, want nil (unconfigured limits allow with warning)", err)
		}
	})

	t.Run("unconfigured_relay_allowed", func(t *testing.T) {
		t.Setenv("CATS_RELAY_ADMIN_URL", "")
		t.Setenv("CATS_RELAY_ADMIN_TOKEN", "")
		if err := NewServiceBalanceCheckerFromEnv().Check(ctx, 38); err != nil {
			t.Fatalf("err = %v, want nil when relay admin is not configured", err)
		}
	})
}
