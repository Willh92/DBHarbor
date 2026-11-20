package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type failingCloser struct {
	err error
}

func (f failingCloser) Close() error {
	return f.err
}

func TestCloseAndFinalizeExportFileRemovesOnCloseError(t *testing.T) {
	target := filepath.Join(t.TempDir(), "export.sql")
	if err := os.WriteFile(target, []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := closeAndFinalizeExportFile(failingCloser{err: errors.New("disk full")}, target); err == nil {
		t.Fatal("expected closeAndFinalizeExportFile to return close error")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected target file %s to be removed after Close error, stat err=%v", target, err)
	}

	okTarget := filepath.Join(t.TempDir(), "ok.sql")
	if err := os.WriteFile(okTarget, []byte("ok"), 0o600); err != nil {
		t.Fatalf("WriteFile okTarget: %v", err)
	}
	if err := closeAndFinalizeExportFile(failingCloser{err: nil}, okTarget); err != nil {
		t.Fatalf("expected nil error on clean Close, got %v", err)
	}
	if _, err := os.Stat(okTarget); err != nil {
		t.Fatalf("expected okTarget to remain when Close succeeds, got %v", err)
	}
}
