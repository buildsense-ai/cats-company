package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const botModelEncryptionKeyEnv = "CATSCO_MODEL_CONFIG_ENCRYPTION_KEY"

type botModelSecretCodec struct {
	aead cipher.AEAD
}

func newBotModelSecretCodecFromEnv() (*botModelSecretCodec, error) {
	raw := strings.TrimSpace(os.Getenv(botModelEncryptionKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is not configured", botModelEncryptionKeyEnv)
	}
	key, err := decodeBotModelEncryptionKey(raw)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create bot model cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create bot model GCM: %w", err)
	}
	return &botModelSecretCodec{aead: aead}, nil
}

func decodeBotModelEncryptionKey(raw string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		key, err := decode(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("CATSCO_MODEL_CONFIG_ENCRYPTION_KEY must encode exactly 32 bytes using base64 or hex")
}

func (c *botModelSecretCodec) encrypt(botUID int64, plaintext []byte) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("bot model secret encryption is unavailable")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("create bot model nonce: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, botModelSecretAAD(botUID))
	payload := append(nonce, sealed...)
	return "v1:" + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (c *botModelSecretCodec) decrypt(botUID int64, ciphertext string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("bot model secret encryption is unavailable")
	}
	version, encoded, ok := strings.Cut(strings.TrimSpace(ciphertext), ":")
	if !ok || version != "v1" || encoded == "" {
		return nil, errors.New("unsupported bot model secret format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid bot model secret encoding")
	}
	nonceSize := c.aead.NonceSize()
	if len(payload) <= nonceSize {
		return nil, errors.New("invalid bot model secret payload")
	}
	plaintext, err := c.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], botModelSecretAAD(botUID))
	if err != nil {
		return nil, errors.New("bot model secret authentication failed")
	}
	return plaintext, nil
}

func botModelSecretAAD(botUID int64) []byte {
	return []byte("catsco:bot-model:" + strconv.FormatInt(botUID, 10))
}
