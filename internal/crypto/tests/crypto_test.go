package crypto_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fitbase/fitbase/internal/crypto"
)

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("super secret token value")

	enc, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := crypto.Decrypt(key, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("got %q, want %q", got, plaintext)
	}
}

func TestEncryptDecrypt_UniqueEachCall(t *testing.T) {
	key := make([]byte, 32)
	plaintext := []byte("same plaintext")

	enc1, _ := crypto.Encrypt(key, plaintext)
	enc2, _ := crypto.Encrypt(key, plaintext)
	if enc1 == enc2 {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonce reuse?)")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key2[0] = 0xFF

	enc, _ := crypto.Encrypt(key1, []byte("secret"))
	_, err := crypto.Decrypt(key2, enc)
	if err == nil {
		t.Error("expected error decrypting with wrong key, got nil")
	}
}

func TestDecrypt_TooShort(t *testing.T) {
	key := make([]byte, 32)
	// base64 of fewer bytes than the GCM nonce size (12)
	_, err := crypto.Decrypt(key, "dG9vc2hvcnQ=") // "tooshort" = 8 bytes
	if err == nil {
		t.Error("expected error for too-short ciphertext, got nil")
	}
}

func TestDecrypt_InvalidBase64(t *testing.T) {
	key := make([]byte, 32)
	_, err := crypto.Decrypt(key, "!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func TestLoadOrCreateKey_CreatesNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	key, err := crypto.LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
	// File must exist and be readable.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if string(data) != string(key) {
		t.Error("key in file does not match returned key")
	}
}

func TestLoadOrCreateKey_LoadsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	// Create initial key.
	key1, _ := crypto.LoadOrCreateKey(path)
	// Load it again — must return the same bytes.
	key2, err := crypto.LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("unexpected error on second load: %v", err)
	}
	if string(key1) != string(key2) {
		t.Error("second load returned different key")
	}
}

func TestLoadOrCreateKey_WrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte("tooshort"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := crypto.LoadOrCreateKey(path)
	if err == nil {
		t.Error("expected error for wrong-size key file, got nil")
	}
}
