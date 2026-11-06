package crypto

import (
	"testing"
)

func TestVaultEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	vault, err := NewVaultFromDataDir(dir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}

	secret := "ClickHouse#2026!VaultSecret$%^&*()_UTF8"
	enc, err := vault.Encrypt(secret)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}
	if enc == secret || enc == "" {
		t.Fatalf("expected non-empty encrypted output, got %q", enc)
	}

	// Reload from same dataDir to verify persistence
	vault2, err := NewVaultFromDataDir(dir, "")
	if err != nil {
		t.Fatalf("reload vault failed: %v", err)
	}
	dec, err := vault2.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if dec != secret {
		t.Fatalf("expected %q, got %q", secret, dec)
	}

	// Tamper detection
	if _, err := vault2.Decrypt(enc[:len(enc)-4] + "AAAA"); err == nil {
		t.Fatalf("expected error on tampered ciphertext")
	}
}
