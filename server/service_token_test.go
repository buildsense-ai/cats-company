package server

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceTokenIssueAndVerify(t *testing.T) {
	secret := "s3cr3t"
	tok := IssueServiceToken(secret, ServiceTokenClaims{
		CallerUID: 441, Agent: "hello", Models: []string{"gpt-6.1-sol"}, RunID: "run_a",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if !strings.HasPrefix(tok, "st1.") {
		t.Fatalf("prefix: %s", tok)
	}
	claims, err := VerifyServiceToken(secret, tok)
	if err != nil || claims.CallerUID != 441 || claims.Agent != "hello" || claims.RunID != "run_a" || len(claims.Models) != 1 || claims.Models[0] != "gpt-6.1-sol" {
		t.Fatalf("verify: %+v %v", claims, err)
	}
	if _, err := VerifyServiceToken("other", tok); err == nil {
		t.Fatal("tampered secret must fail")
	}
	expired := IssueServiceToken(secret, ServiceTokenClaims{CallerUID: 1, ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := VerifyServiceToken(secret, expired); !errors.Is(err, ErrServiceTokenExpired) {
		t.Fatalf("expired: %v", err)
	}
}

func TestServiceTokenFormatRejections(t *testing.T) {
	secret := "s3cr3t"
	tok := IssueServiceToken(secret, ServiceTokenClaims{CallerUID: 441, ExpiresAt: time.Now().Add(time.Hour)})
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token shape: %s", tok)
	}
	if _, err := VerifyServiceToken(secret, ""); !errors.Is(err, ErrServiceTokenFormat) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := VerifyServiceToken(secret, "xx1."+parts[1]+"."+parts[2]); !errors.Is(err, ErrServiceTokenFormat) {
		t.Fatalf("bad prefix: %v", err)
	}
	if _, err := VerifyServiceToken(secret, parts[0]+"."+parts[1]); !errors.Is(err, ErrServiceTokenFormat) {
		t.Fatalf("missing sig: %v", err)
	}
}

func TestServiceTokenTamperedPayloadRejected(t *testing.T) {
	secret := "s3cr3t"
	tok := IssueServiceToken(secret, ServiceTokenClaims{CallerUID: 441, ExpiresAt: time.Now().Add(time.Hour)})
	parts := strings.Split(tok, ".")
	b := []byte(parts[1])
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	tampered := parts[0] + "." + string(b) + "." + parts[2]
	if _, err := VerifyServiceToken(secret, tampered); err == nil {
		t.Fatal("tampered payload must fail")
	}
}

// 跨语言互通（计划① Task 5 Step 7）：与 relay(Python) 侧 tests/test_service_tokens.py
// 使用同一金样；两侧同参数生成的 token 字节级一致。
const serviceTokenInteropGolden = "st1.eyJ2IjoxLCJjYWxsZXJfdWlkIjo0NDEsImFnZW50IjoiaGVsbG8iLCJtb2RlbHMiOlsiZ3B0LTYuMS1zb2wiXSwicnVuX2lkIjoicnVuX2ludGVyb3AiLCJwdXJwb3NlIjoic2VydmljZSIsImlhdCI6MTc2MDAwMDAwMCwiZXhwIjoxODkzNDU2MDAwfQ.d1B__RNDXLkvpjQIBpLUSh2-wb93o-k_2lhelyykqKU"

func TestServiceTokenPythonInterop(t *testing.T) {
	secret := "catsco-interop-secret"
	claims, err := VerifyServiceToken(secret, serviceTokenInteropGolden)
	if err != nil || claims.CallerUID != 441 || claims.Agent != "hello" {
		t.Fatalf("verify interop golden: %+v %v", claims, err)
	}
	reproduced := IssueServiceToken(secret, ServiceTokenClaims{
		CallerUID: 441, Agent: "hello", Models: []string{"gpt-6.1-sol"}, RunID: "run_interop",
		IssuedAt: time.Unix(1760000000, 0), ExpiresAt: time.Unix(1893456000, 0),
	})
	if reproduced != serviceTokenInteropGolden {
		t.Fatalf("go reproduction diverges from interop golden:\n got %s\nwant %s", reproduced, serviceTokenInteropGolden)
	}
}
