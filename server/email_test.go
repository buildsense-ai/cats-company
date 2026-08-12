package server

import (
	"strings"
	"testing"
	"time"
)

func TestBuildTencentCloudAuthorizationMatchesOfficialExample(t *testing.T) {
	payload := `{"Limit": 1, "Filters": [{"Values": ["\u672a\u547d\u540d"], "Name": "instance-name"}]}`
	auth := buildTencentCloudAuthorization(
		"AKID********************************",
		"********************************",
		"cvm",
		"cvm.tencentcloudapi.com",
		"DescribeInstances",
		payload,
		1551113065,
	)

	const want = "Signature=10b1a37a7301a02ca19a647ad722d5e43b4b3cff309d421d85b46093f6ab6c4f"
	if !strings.Contains(auth, want) {
		t.Fatalf("authorization signature mismatch\nwant contains: %s\ngot: %s", want, auth)
	}
}

func TestVerificationCodePurposeIsolation(t *testing.T) {
	email := "reset-purpose@example.com"
	code := "123456"

	deleteVerificationCode(email, verificationPurposeRegister)
	deleteVerificationCode(email, verificationPurposePasswordReset)

	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), verificationPurposeRegister)
	if verifyCodeForPurpose(email, code, verificationPurposePasswordReset) {
		t.Fatal("register verification code must not validate password reset purpose")
	}
	if !verifyCode(email, code) {
		t.Fatal("register verification code should validate register purpose")
	}
}

func TestVerificationCodeStatus(t *testing.T) {
	email := "status@example.com"
	code := "654321"
	purpose := verificationPurposeRegister

	deleteVerificationCode(email, purpose)

	// not found
	if got := verificationCodeStatus(email, code, purpose); got != codeStatusNotFound {
		t.Fatalf("expected not found, got %v", got)
	}

	// valid
	storeVerificationCode(email, code, time.Now().Add(time.Minute).Unix(), purpose)
	if got := verificationCodeStatus(email, code, purpose); got != codeStatusValid {
		t.Fatalf("expected valid, got %v", got)
	}

	// mismatch: status is read-only on failure, original code stays valid
	if got := verificationCodeStatus(email, "000000", purpose); got != codeStatusMismatch {
		t.Fatalf("expected mismatch, got %v", got)
	}
	if got := verificationCodeStatus(email, code, purpose); got != codeStatusValid {
		t.Fatalf("expected still valid after mismatch check, got %v", got)
	}

	// expired, and the stale entry is removed
	storeVerificationCode(email, code, time.Now().Add(-time.Minute).Unix(), purpose)
	if got := verificationCodeStatus(email, code, purpose); got != codeStatusExpired {
		t.Fatalf("expected expired, got %v", got)
	}
	if got := verificationCodeStatus(email, code, purpose); got != codeStatusNotFound {
		t.Fatalf("expected not found after expiry removal, got %v", got)
	}
}

func TestIsValidEmailFormat(t *testing.T) {
	valid := []string{
		"user@qq.com",
		"user@example.com.cn",
		"dev@github.io",
		"a@sub.example.dev",
		"UPPER@Example.COM",
	}
	for _, email := range valid {
		if !isValidEmailFormat(email) {
			t.Errorf("expected valid email %q to pass", email)
		}
	}

	invalid := []string{
		"user@qq.cpm",      // cpm is not a real TLD (the reported typo)
		"user@example.c0m", // typo TLD
		"user@localhost",   // no dotted domain
		"@qq.com",          // missing local part
		"no-at-sign",       // missing @
		"user@com",         // domain without a dot
	}
	for _, email := range invalid {
		if isValidEmailFormat(email) {
			t.Errorf("expected invalid email %q to be rejected", email)
		}
	}
}

func TestPasswordResetEmailSubject(t *testing.T) {
	if got := verificationEmailSubject(verificationPurposePasswordReset); got != "Cats Company 重置密码验证码" {
		t.Fatalf("unexpected password reset subject: %s", got)
	}
}
