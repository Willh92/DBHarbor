package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Vault provides AES-256-GCM authenticated encryption for database credentials.
type Vault struct {
	aead cipher.AEAD
}

// NewVaultFromDataDir loads or generates a 32-byte master key in dataDir/.master.key,
// or derives one if explicitSecret is provided.
func NewVaultFromDataDir(dataDir, explicitSecret string) (*Vault, error) {
	var key []byte
	if strings.TrimSpace(explicitSecret) != "" {
		decoded, err := hex.DecodeString(strings.TrimSpace(explicitSecret))
		if err == nil && len(decoded) == 32 {
			key = decoded
		} else {
			sum := sha256.Sum256([]byte(explicitSecret))
			key = sum[:]
		}
		return NewVault(key)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	keyPath := filepath.Join(dataDir, ".master.key")

	raw, err := os.ReadFile(keyPath)
	if err == nil {
		decoded, decErr := hex.DecodeString(strings.TrimSpace(string(raw)))
		if decErr == nil && len(decoded) == 32 {
			return NewVault(decoded)
		}
	}

	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	encoded := hex.EncodeToString(key)
	if err := os.WriteFile(keyPath, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(keyPath, 0o600)
	}
	return NewVault(key)
}

// NewVault creates a Vault with a raw 32-byte AES-256 key.
func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("vault key must be 32 bytes for AES-256")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

// Encrypt encrypts plaintext into URL-safe base64 ciphertext. Empty string stays empty.
func (v *Vault) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := v.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts base64 ciphertext produced by Encrypt.
func (v *Vault) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	ns := v.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, enc := raw[:ns], raw[ns:]
	plain, err := v.aead.Open(nil, nonce, enc, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

// KeyFingerprint returns a short non-reversible identifier suitable for deriving JWT signing keys.
func (v *Vault) DeriveBytes(purpose string) []byte {
	enc, _ := v.aead.Seal(nil, make([]byte, v.aead.NonceSize()), []byte("dbharbor:"+purpose), nil), error(nil)
	sum := sha256.Sum256(enc)
	return sum[:]
}

// NewUUID generates a RFC 4122 version 4 UUID using crypto/rand without external dependencies.
func NewUUID() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant RFC 4122
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
