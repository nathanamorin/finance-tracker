package keystore

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestLoadOrCreateRoundTrip(t *testing.T) {
	ks, err := New(newKey(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dek.json")

	dek1, created, err := ks.LoadOrCreate(path, "alice")
	if err != nil || !created || len(dek1) != DEKSize {
		t.Fatalf("create: dek=%d created=%v err=%v", len(dek1), created, err)
	}
	dek2, created, err := ks.LoadOrCreate(path, "alice")
	if err != nil || created {
		t.Fatalf("load: created=%v err=%v", created, err)
	}
	if !bytes.Equal(dek1, dek2) {
		t.Fatal("reloaded key differs")
	}
}

func TestWrongUserOrMasterKeyFails(t *testing.T) {
	ks, _ := New(newKey(t), nil)
	path := filepath.Join(t.TempDir(), "dek.json")
	if _, _, err := ks.LoadOrCreate(path, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ks.LoadOrCreate(path, "mallory"); !errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("other user: want ErrNoUsableKey, got %v", err)
	}
	other, _ := New(newKey(t), nil)
	if _, _, err := other.LoadOrCreate(path, "alice"); !errors.Is(err, ErrNoUsableKey) {
		t.Fatalf("other master key: want ErrNoUsableKey, got %v", err)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newMaster := newKey(t), newKey(t)
	path := filepath.Join(t.TempDir(), "dek.json")

	oldKS, _ := New(oldKey, nil)
	dek, _, err := oldKS.LoadOrCreate(path, "alice")
	if err != nil {
		t.Fatal(err)
	}

	rotated, _ := New(newMaster, []string{oldKey})
	got, _, err := rotated.LoadOrCreate(path, "alice")
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("rotated load: err=%v equal=%v", err, bytes.Equal(got, dek))
	}

	// After re-wrap the old key is no longer needed.
	newOnly, _ := New(newMaster, nil)
	got, _, err = newOnly.LoadOrCreate(path, "alice")
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("post-rotation load: err=%v", err)
	}
}

func TestBadMasterKey(t *testing.T) {
	if _, err := New("c2hvcnQ=", nil); err == nil {
		t.Fatal("expected error for short key")
	}
}
