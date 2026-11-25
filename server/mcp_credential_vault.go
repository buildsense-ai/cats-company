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

// mcpCredentialEncryptionKeyEnv 是凭据托管加密密钥的环境变量。
//
// 值用 base64 / hex 编码 32 字节（AES-256-GCM）。与 bot model 密钥分离：
// 凭据托管的数据面更广（OAuth access/refresh token、client secret），
// 轮换半径越小越好。未配置时加密写入一律失败 —— 宁可拒绝存凭据，
// 也不落一份明文进库。
const mcpCredentialEncryptionKeyEnv = "CATSCO_MCP_CREDENTIAL_ENCRYPTION_KEY"

// mcpCredentialVault 负责 console MCP server 凭据的加密托管（设计：加密凭据托管）。
//
// 加密口径：AES-256-GCM，AAD 绑定 (owner_uid, server_id, field) —— 换绑账号/服务端点
// 或跨字段搬动密文都会让解密直接失败，密文复制粘贴攻击在密码学层面就失效。
// 编码格式："v1:<base64url(nonce|ciphertext)>"，版本前缀留给未来的轮换。
type mcpCredentialVault struct {
	aead cipher.AEAD
}

// NewMCPCredentialVaultFromEnv 用环境密钥构造托管加密器。
// 未配置/坏密钥 = 报错；装配层据此决定「凭据托管不可用」（该写明文的路径不存在）。
func NewMCPCredentialVaultFromEnv() (*mcpCredentialVault, error) {
	raw := strings.TrimSpace(os.Getenv(mcpCredentialEncryptionKeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s is not configured", mcpCredentialEncryptionKeyEnv)
	}
	key, err := decodeMCPCredentialKey(raw)
	if err != nil {
		return nil, err
	}
	return NewMCPCredentialVault(key)
}

// NewMCPCredentialVault 用显式密钥构造（测试注入）。
func NewMCPCredentialVault(key []byte) (*mcpCredentialVault, error) {
	if len(key) != 32 {
		return nil, errors.New("mcp credential key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create mcp credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create mcp credential GCM: %w", err)
	}
	return &mcpCredentialVault{aead: aead}, nil
}

func decodeMCPCredentialKey(raw string) ([]byte, error) {
	for _, decode := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		hex.DecodeString,
	} {
		key, err := decode(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("CATSCO_MCP_CREDENTIAL_ENCRYPTION_KEY must encode exactly 32 bytes using base64 or hex")
}

// vaultAAD 把密文钉死在 (owner, server, field) 三元组上：
// 攻击者拿不到可移植的密文 —— 偷出来贴到别的账号/别的字段上解不开。
func vaultAAD(ownerUID, serverID int64, field string) []byte {
	return []byte("catsco:mcp-credential:" + strconv.FormatInt(ownerUID, 10) +
		":" + strconv.FormatInt(serverID, 10) + ":" + field)
}

// Encrypt 加密一份凭据明文。空明文不加密，直接返回空串（调用方按「无凭据」处理）。
func (v *mcpCredentialVault) Encrypt(ownerUID, serverID int64, field string, plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", nil
	}
	if v == nil || v.aead == nil {
		return "", errors.New("mcp credential vault is unavailable")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("create mcp credential nonce: %w", err)
	}
	sealed := v.aead.Seal(nil, nonce, plaintext, vaultAAD(ownerUID, serverID, field))
	payload := append(nonce, sealed...)
	return "v1:" + base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decrypt 解密一份凭据密文。空密文返回空明文（无凭据语义）。
func (v *mcpCredentialVault) Decrypt(ownerUID, serverID int64, field, ciphertext string) ([]byte, error) {
	if ciphertext == "" {
		return nil, nil
	}
	if v == nil || v.aead == nil {
		return nil, errors.New("mcp credential vault is unavailable")
	}
	version, encoded, ok := strings.Cut(strings.TrimSpace(ciphertext), ":")
	if !ok || version != "v1" || encoded == "" {
		return nil, errors.New("unsupported mcp credential ciphertext format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("invalid mcp credential ciphertext encoding")
	}
	nonceSize := v.aead.NonceSize()
	if len(payload) <= nonceSize {
		return nil, errors.New("invalid mcp credential ciphertext payload")
	}
	plaintext, err := v.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], vaultAAD(ownerUID, serverID, field))
	if err != nil {
		return nil, errors.New("mcp credential ciphertext authentication failed")
	}
	return plaintext, nil
}
