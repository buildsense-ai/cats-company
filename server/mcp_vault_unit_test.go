package server

import (
	"strings"
	"testing"
)

// 单元层：vault 的格式/边界行为（不依赖 console handler）。
func TestMCPCredentialVaultEdges(t *testing.T) {
	vault := testVault(t)

	// 空明文不加密（无凭据语义）。
	enc, err := vault.Encrypt(1, 2, "access_token", nil)
	if err != nil || enc != "" {
		t.Fatalf("empty plaintext: enc=%q err=%v", enc, err)
	}
	plain, err := vault.Decrypt(1, 2, "access_token", "")
	if err != nil || plain != nil {
		t.Fatalf("empty ciphertext: err=%v", err)
	}

	// 损坏的密文必须报错而不是返回垃圾。
	good, err := vault.Encrypt(1, 2, "access_token", []byte("real-token"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"v0:abc", "v1:", "v1:!!!notbase64", "v1:AAAA",
		good[:len(good)-2] + "xx", // 尾字节被改：GCM 认证必须失败
	} {
		if _, err := vault.Decrypt(1, 2, "access_token", bad); err == nil {
			t.Fatalf("corrupted ciphertext %q must fail", bad)
		}
	}

	// 密钥长度校验。
	if _, err := NewMCPCredentialVault([]byte("short")); err == nil {
		t.Fatal("short key must be rejected")
	}
	// 密文里没有明文碎片。
	if strings.Contains(good, "real-token") {
		t.Fatal("ciphertext contains plaintext")
	}
}
