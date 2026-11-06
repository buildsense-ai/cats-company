package server

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisserver "github.com/alicebob/miniredis/v2/server"
)

func TestRedisGrantStorageHonorsSocketDeadline(t *testing.T) {
	for _, operation := range []string{"get", "set", "exists"} {
		t.Run(operation, func(t *testing.T) {
			mr := miniredis.RunT(t)
			state := newRedisRuntimeStateForTest(t, "redis://"+mr.Addr(), "socket-timeout-"+operation)
			defer state.Close()
			if !state.client.Options().ContextTimeoutEnabled {
				t.Fatal("socket context deadlines disabled")
			}
			// Actual server-side stall, not a client hook that merely observes ctx.
			// Without ContextTimeoutEnabled this falls back to the 3s socket timeout.
			mr.Server().SetPreHook(func(_ *redisserver.Peer, cmd string, _ ...string) bool {
				if strings.EqualFold(cmd, operation) {
					time.Sleep(600 * time.Millisecond)
				}
				return false
			})
			start := time.Now()
			switch operation {
			case "get":
				if _, ok := state.lookupDeviceGrant("private-grant", start.UnixMilli()); ok {
					t.Fatal("slow grant read authorized")
				}
			case "set":
				state.rememberDeviceGrants([]ScopedDeviceGrant{{GrantID: "private-grant", ExpiresAt: start.Add(time.Minute).UnixMilli()}}, start.UnixMilli())
			case "exists":
				if !state.isDeviceConnectorRevoked(&DeviceConnectorClaims{UID: 7, DeviceID: "private-device"}, start) {
					t.Fatal("slow revocation check authorized")
				}
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("authorization storage operation exceeded bounded budget: %s", elapsed)
			}
		})
	}
}

func TestRedisAuthorizationFailureLogsAreDistinctRedactedAndRateLimited(t *testing.T) {
	mr := miniredis.RunT(t)
	state := newRedisRuntimeStateForTest(t, "redis://"+mr.Addr(), "redacted-logs")
	defer state.Close()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	now := time.Now()
	claims := &DeviceConnectorClaims{UID: 7, DeviceID: "PRIVATE_DEVICE"}
	claims.ID = "PRIVATE_TOKEN"
	// Normal absence is not an incident.
	if state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("missing watermark should not revoke")
	}
	if _, ok := state.lookupDeviceGrant("PRIVATE_GRANT", now.UnixMilli()); ok {
		t.Fatal("missing grant authorized")
	}
	if output.Len() != 0 {
		t.Fatal("normal missing state logged as storage failure")
	}
	if err := state.client.Set(context.Background(), state.deviceConnectorRevokedDeviceKey(7, claims.DeviceID), "PRIVATE_VALUE", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if !state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("corrupt watermark authorized")
	}
	if !strings.Contains(output.String(), "operation=revoked_device_read reason=invalid_watermark") {
		t.Fatal("corruption reason absent")
	}
	mr.Server().SetPreHook(func(peer *redisserver.Peer, cmd string, _ ...string) bool {
		if strings.EqualFold(cmd, "get") {
			peer.WriteError("ERR PRIVATE_DEVICE_READ_ERROR")
			return true
		}
		return false
	})
	if !state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("failed device watermark read authorized")
	}
	if !strings.Contains(output.String(), "operation=revoked_device_read reason=redis_error") {
		t.Fatal("watermark storage error not distinguished from corrupt data")
	}
	// Raw error may contain credentials or payloads: log only its category.
	mr.SetError("OOM PRIVATE_ERROR redis://user:PRIVATE_PASSWORD@host")
	state.rememberDeviceGrants([]ScopedDeviceGrant{{GrantID: "PRIVATE_GRANT", ExpiresAt: now.Add(time.Minute).UnixMilli()}}, now.UnixMilli())
	state.lookupDeviceGrant("PRIVATE_GRANT", now.UnixMilli())
	for i := 0; i < 3; i++ {
		if !state.isDeviceConnectorRevoked(claims, now) {
			t.Fatal("storage error authorized")
		}
	}
	text := output.String()
	for _, operation := range []string{"grant_write", "grant_read", "revoked_token_read"} {
		if strings.Count(text, "operation="+operation+" reason=out_of_memory") != 1 {
			t.Fatalf("missing/duplicate category %s", operation)
		}
	}
	if strings.Contains(text, "PRIVATE_") || strings.Contains(text, "redis://") {
		t.Fatal("sensitive state escaped logs")
	}
}
