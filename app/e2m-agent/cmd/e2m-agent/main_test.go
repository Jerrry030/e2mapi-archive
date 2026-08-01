package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadSecretFile(t *testing.T) {
	t.Run("reads and trims regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "enrollment.token")
		if err := os.WriteFile(path, []byte("  enrollment-token\r\n"), 0600); err != nil {
			t.Fatalf("write secret: %v", err)
		}
		got, err := readSecretFile(path)
		if err != nil {
			t.Fatalf("read secret: %v", err)
		}
		if got != "enrollment-token" {
			t.Fatalf("secret = %q", got)
		}
	})

	t.Run("rejects non-regular file", func(t *testing.T) {
		if _, err := readSecretFile(t.TempDir()); err == nil {
			t.Fatal("expected directory secret path to be rejected")
		}
	})

	t.Run("rejects oversized file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "enrollment.token")
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), (64<<10)+1), 0600); err != nil {
			t.Fatalf("write oversized secret: %v", err)
		}
		if _, err := readSecretFile(path); err == nil {
			t.Fatal("expected oversized secret file to be rejected")
		}
	})

	t.Run("missing path reports not exist", func(t *testing.T) {
		if _, err := readSecretFile(""); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty path error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestReadSecretFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.token")
	link := filepath.Join(dir, "enrollment.token")
	if err := os.WriteFile(target, []byte("enrollment-token\n"), 0600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readSecretFile(link); err == nil {
		t.Fatal("expected symlink secret path to be rejected")
	}
}
