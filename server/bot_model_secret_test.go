package server

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBotModelSecretCodecEncryptsAndAuthenticatesByBot(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	codec, err := newBotModelSecretCodecFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := codec.encrypt(43, []byte(`{"api_key":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, "secret") {
		t.Fatal("ciphertext exposed plaintext")
	}
	plaintext, err := codec.decrypt(43, ciphertext)
	if err != nil || string(plaintext) != `{"api_key":"secret"}` {
		t.Fatalf("plaintext=%q err=%v", plaintext, err)
	}
	if _, err := codec.decrypt(44, ciphertext); err == nil {
		t.Fatal("another bot must not decrypt this secret")
	}
}

func TestBotModelSecretCodecRejectsMissingOrWeakKey(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, "")
	if _, err := newBotModelSecretCodecFromEnv(); err == nil {
		t.Fatal("missing key should fail")
	}
	t.Setenv(botModelEncryptionKeyEnv, base64.StdEncoding.EncodeToString([]byte("too-short")))
	if _, err := newBotModelSecretCodecFromEnv(); err == nil {
		t.Fatal("short key should fail")
	}
}
