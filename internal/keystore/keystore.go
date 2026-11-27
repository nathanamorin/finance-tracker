// Package keystore manages per-user database encryption keys using
// envelope encryption.
//
// Every user gets a random 64-byte data-encryption key (DEK), used as the
// AES-256-XTS key for their SQLite database (see internal/store). The DEK is
// never stored in plaintext: it is wrapped with AES-256-GCM under a
// server-wide key-encryption key (KEK, the "master key", supplied via a
// Kubernetes Secret) and written next to the database as dek.json.
//
// The user ID is bound into the GCM additional data, so a wrapped key file
// copied into another user's directory fails to unwrap rather than silently
// granting access.
//
// Rotating the master key: set the new key as the current master key and
// list the old one in the previous keys. Wrapped DEKs that only open with a
// previous key are transparently re-wrapped under the current key the next
// time that user's database is opened. The databases themselves never need
// re-encrypting, since the DEK does not change.
package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DEKSize is the size of a data-encryption key: 64 bytes selects
// AES-256-XTS in the xts SQLite VFS.
const DEKSize = 64

const wrapVersion = 1

// ErrNoUsableKey is returned when a wrapped DEK exists but none of the
// configured master keys can unwrap it.
var ErrNoUsableKey = errors.New("keystore: no configured master key can unwrap this user's data key")

type masterKey struct {
	id   string // short fingerprint, stored alongside wrapped keys
	aead cipher.AEAD
}

// KeyStore wraps and unwraps per-user DEKs.
type KeyStore struct {
	current  masterKey
	previous []masterKey
}

type wrappedKey struct {
	Version    int    `json:"version"`
	KEKID      string `json:"kek_id"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// New builds a KeyStore from base64-encoded 32-byte master keys.
func New(currentB64 string, previousB64 []string) (*KeyStore, error) {
	cur, err := parseMasterKey(currentB64)
	if err != nil {
		return nil, fmt.Errorf("current master key: %w", err)
	}
	ks := &KeyStore{current: cur}
	for i, p := range previousB64 {
		if p == "" {
			continue
		}
		mk, err := parseMasterKey(p)
		if err != nil {
			return nil, fmt.Errorf("previous master key %d: %w", i, err)
		}
		ks.previous = append(ks.previous, mk)
	}
	return ks, nil
}

func parseMasterKey(b64 string) (masterKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return masterKey{}, fmt.Errorf("not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return masterKey{}, fmt.Errorf("must decode to 32 bytes, got %d (generate with: openssl rand -base64 32)", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return masterKey{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return masterKey{}, err
	}
	sum := sha256.Sum256(append([]byte("fin-tracker/kek-id/"), raw...))
	return masterKey{id: hex.EncodeToString(sum[:4]), aead: aead}, nil
}

func aad(userID string) []byte {
	return []byte("fin-tracker/dek/v1|" + userID)
}

// LoadOrCreate returns the plaintext DEK stored at path for userID,
// generating and persisting a new one if the file does not exist yet.
// created reports whether a new key was generated.
func (ks *KeyStore) LoadOrCreate(path, userID string) (dek []byte, created bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		dek = make([]byte, DEKSize)
		if _, err := rand.Read(dek); err != nil {
			return nil, false, err
		}
		if err := ks.write(path, userID, dek); err != nil {
			return nil, false, err
		}
		return dek, true, nil
	}
	if err != nil {
		return nil, false, err
	}

	var w wrappedKey
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, false, fmt.Errorf("keystore: corrupt key file %s: %w", path, err)
	}
	if w.Version != wrapVersion {
		return nil, false, fmt.Errorf("keystore: unsupported key file version %d", w.Version)
	}
	nonce, err := base64.StdEncoding.DecodeString(w.Nonce)
	if err != nil {
		return nil, false, fmt.Errorf("keystore: corrupt nonce: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(w.Ciphertext)
	if err != nil {
		return nil, false, fmt.Errorf("keystore: corrupt ciphertext: %w", err)
	}

	// Try the current key first, then any previous keys (rotation).
	candidates := append([]masterKey{ks.current}, ks.previous...)
	for i, mk := range candidates {
		dek, err := mk.aead.Open(nil, nonce, ct, aad(userID))
		if err != nil {
			continue
		}
		if i > 0 {
			// Opened with a retired key: re-wrap under the current one.
			if err := ks.write(path, userID, dek); err != nil {
				return nil, false, fmt.Errorf("keystore: re-wrapping key under current master key: %w", err)
			}
		}
		return dek, false, nil
	}
	return nil, false, ErrNoUsableKey
}

func (ks *KeyStore) write(path, userID string, dek []byte) error {
	nonce := make([]byte, ks.current.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	w := wrappedKey{
		Version:    wrapVersion,
		KEKID:      ks.current.id,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ks.current.aead.Seal(nil, nonce, dek, aad(userID))),
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically so a crash can never leave a truncated key file,
	// which would make the user's database permanently unreadable.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dek-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
