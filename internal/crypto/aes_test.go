package crypto

import (
	"bytes"
	"testing"
)

// TestRoundTrip verifies that encrypting then decrypting returns the original
// plaintext.
func TestRoundTrip(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	plaintext := []byte("hello, EFB connector")

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

// TestWrongKey ensures that decryption with a different key fails.
func TestWrongKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	ciphertext, err := Encrypt([]byte("secret"), key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	wrongKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (wrong key): %v", err)
	}

	_, err = Decrypt(ciphertext, wrongKey)
	if err == nil {
		t.Error("expected error when decrypting with wrong key, got nil")
	}
}

// TestTamperedCiphertext ensures that modifying any byte of the ciphertext
// (beyond the nonce) causes decryption to fail.
func TestTamperedCiphertext(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	ciphertext, err := Encrypt([]byte("tamper me"), key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a bit in the first byte of the actual ciphertext (after the nonce).
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[nonceSize] ^= 0xFF

	_, err = Decrypt(tampered, key)
	if err == nil {
		t.Error("expected error for tampered ciphertext, got nil")
	}
}

// TestEmptyPlaintext ensures that empty plaintext encrypts and decrypts
// correctly.
func TestEmptyPlaintext(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	ciphertext, err := Encrypt([]byte{}, key)
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	got, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("expected empty plaintext, got %q", got)
	}
}

// TestShortCiphertext ensures that a ciphertext shorter than the nonce size
// returns an error without panicking.
func TestShortCiphertext(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	for _, length := range []int{0, 1, nonceSize - 1} {
		_, err := Decrypt(make([]byte, length), key)
		if err == nil {
			t.Errorf("expected error for ciphertext of length %d, got nil", length)
		}
	}
}

// TestKeySize verifies that non-32-byte keys are rejected by both Encrypt and
// Decrypt.
func TestKeySize(t *testing.T) {
	plaintext := []byte("test")
	validKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	ciphertext, err := Encrypt(plaintext, validKey)
	if err != nil {
		t.Fatalf("Encrypt with valid key: %v", err)
	}

	badSizes := []int{0, 1, 16, 24, 31, 33, 64}
	for _, size := range badSizes {
		badKey := make([]byte, size)

		if _, err := Encrypt(plaintext, badKey); err == nil {
			t.Errorf("Encrypt: expected error for key size %d, got nil", size)
		}

		if _, err := Decrypt(ciphertext, badKey); err == nil {
			t.Errorf("Decrypt: expected error for key size %d, got nil", size)
		}
	}
}

// TestGenerateKeyLength checks that GenerateKey returns exactly 32 bytes.
func TestGenerateKeyLength(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(key) != keySize {
		t.Errorf("expected key length %d, got %d", keySize, len(key))
	}
}

// TestNonceUniqueness verifies that two encryptions of the same plaintext
// produce different ciphertexts (different nonces).
func TestNonceUniqueness(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	plaintext := []byte("same plaintext")

	ct1, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 1: %v", err)
	}

	ct2, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt 2: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext produced identical ciphertexts (nonces not random)")
	}
}
