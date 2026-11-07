package store

import (
	"testing"

	"dbharbor/backend/internal/crypto"
)

func TestOpenCustomAdminBootstrapAndValidation(t *testing.T) {
	invalidDir := t.TempDir()
	vault1, err := crypto.NewVaultFromDataDir(invalidDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	if _, err := Open(invalidDir, vault1, "a", "StrongPass123"); err == nil {
		t.Fatal("expected 1-char admin username to fail validation")
	}
	if _, err := Open(invalidDir, vault1, "ops_admin", "123"); err == nil {
		t.Fatal("expected <6 char custom admin password to fail validation")
	}

	validDir := t.TempDir()
	vault2, err := crypto.NewVaultFromDataDir(validDir, "")
	if err != nil {
		t.Fatalf("NewVaultFromDataDir failed: %v", err)
	}
	st, err := Open(validDir, vault2, "ops.admin@corp", "Secr3t!2026")
	if err != nil {
		t.Fatalf("Open with custom admin failed: %v", err)
	}

	u, err := st.Authenticate("ops.admin@corp", "Secr3t!2026")
	if err != nil {
		_ = st.Close()
		t.Fatalf("Authenticate custom admin failed: %v", err)
	}
	if u.Role != RoleAdmin || !u.PasswordChanged {
		_ = st.Close()
		t.Fatalf("expected RoleAdmin with PasswordChanged=true, got role=%q passwordChanged=%v", u.Role, u.PasswordChanged)
	}
	if _, err := st.Authenticate("admin", "admin123"); err == nil {
		_ = st.Close()
		t.Fatal("default admin account should not exist when custom admin was initialized")
	}
	_ = st.Close()

	// Re-opening existing DB with different bootstrap params must NOT overwrite existing users
	st2, err := Open(validDir, vault2, "another_admin", "AnotherPass99")
	if err != nil {
		t.Fatalf("Re-open existing store failed: %v", err)
	}
	defer st2.Close()
	if _, err := st2.Authenticate("ops.admin@corp", "Secr3t!2026"); err != nil {
		t.Fatalf("expected original custom admin to remain intact after re-open: %v", err)
	}
	if _, err := st2.Authenticate("another_admin", "AnotherPass99"); err == nil {
		t.Fatal("expected ensureAdmin to be a no-op once users table is already populated")
	}
}
