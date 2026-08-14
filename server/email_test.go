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

func TestPasswordResetEmailSubject(t *testing.T) {
	if got := verificationEmailSubject(verificationPurposePasswordReset); got != "Cats Company 重置密码验证码" {
		t.Fatalf("unexpected password reset subject: %s", got)
	}
}
