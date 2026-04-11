package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"strings"
	"testing"
)

func TestGenerateAESKeyIV(t *testing.T) {
	key, iv, err := generateAESKeyIV()
	if err != nil {
		t.Fatalf("generateAESKeyIV failed: %v", err)
	}

	if len(key) != 16 {
		t.Errorf("key length = %d, want 16", len(key))
	}
	if len(iv) != 16 {
		t.Errorf("iv length = %d, want 16", len(iv))
	}

	// Verify valid hex
	if _, err := hex.DecodeString(string(key)); err != nil {
		t.Errorf("key is not valid hex: %v", err)
	}
	if _, err := hex.DecodeString(string(iv)); err != nil {
		t.Errorf("iv is not valid hex: %v", err)
	}

	// Verify uniqueness
	key2, iv2, err := generateAESKeyIV()
	if err != nil {
		t.Fatalf("generateAESKeyIV (2nd call) failed: %v", err)
	}
	if string(key) == string(key2) {
		t.Error("two generated keys should not be identical")
	}
	if string(iv) == string(iv2) {
		t.Error("two generated IVs should not be identical")
	}
}

func TestPKCS7PadUnpad(t *testing.T) {
	tests := []struct {
		name string
		len  int
	}{
		{"empty", 0},
		{"one byte", 1},
		{"15 bytes", 15},
		{"16 bytes (block size)", 16},
		{"17 bytes", 17},
		{"32 bytes (2 blocks)", 32},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, tt.len)
			for i := range data {
				data[i] = byte(i % 256)
			}
			padded := pkcs7Pad(data, 16)
			if len(padded)%16 != 0 {
				t.Errorf("padded length %d is not multiple of 16", len(padded))
			}
			unpadded := pkcs7Unpad(padded)
			if len(unpadded) != tt.len {
				t.Errorf("unpadded length = %d, want %d", len(unpadded), tt.len)
			}
			for i := range data {
				if unpadded[i] != data[i] {
					t.Errorf("unpadded[%d] = %d, want %d", i, unpadded[i], data[i])
					break
				}
			}
		})
	}
}

func TestAESEncryptDecrypt(t *testing.T) {
	key, iv, _ := generateAESKeyIV()
	plaintext := []byte("Hello, this is a test message for AES encryption!")

	encrypted, err := aesEncrypt(plaintext, key, iv)
	if err != nil {
		t.Fatalf("aesEncrypt failed: %v", err)
	}

	if encrypted == "" {
		t.Fatal("encrypted should not be empty")
	}

	decrypted, err := aesDecrypt(encrypted, key, iv)
	if err != nil {
		t.Fatalf("aesDecrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestAESDecryptInvalidInput(t *testing.T) {
	key, iv, _ := generateAESKeyIV()

	t.Run("non-base64", func(t *testing.T) {
		_, err := aesDecrypt("not-valid-base64!!!", key, iv)
		if err == nil {
			t.Error("expected error for non-base64 input")
		}
	})

	t.Run("wrong block size", func(t *testing.T) {
		_, err := aesDecrypt("AQIDBA==", key, iv)
		if err == nil {
			t.Error("expected error for wrong block size")
		}
	})
}

func TestRSAEncryptDecrypt(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	plaintext := "test password 123"
	encrypted, err := rsaEncrypt(plaintext, privKey.PublicKey.N, privKey.PublicKey.E)
	if err != nil {
		t.Fatalf("rsaEncrypt failed: %v", err)
	}

	if encrypted == "" {
		t.Fatal("encrypted should not be empty")
	}

	// Decrypt using stdlib
	encBytes, err := hex.DecodeString(encrypted)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}

	decrypted, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, encBytes)
	if err != nil {
		t.Fatalf("RSA decrypt failed: %v", err)
	}

	if string(decrypted) != plaintext {
		t.Errorf("round-trip mismatch: got %q, want %q", string(decrypted), plaintext)
	}
}

func TestRSAEncryptMultiChunk(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	// Data longer than modSize-11 (245 bytes for 2048-bit key) to force multi-chunk
	longData := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 8) // 280 bytes
	encrypted, err := rsaEncrypt(longData, privKey.PublicKey.N, privKey.PublicKey.E)
	if err != nil {
		t.Fatalf("rsaEncrypt failed: %v", err)
	}

	// Multiple chunks means encrypted should be longer than one modulus
	modSize := privKey.Size()
	encBytes, err := hex.DecodeString(encrypted)
	if err != nil {
		t.Fatalf("hex decode failed: %v", err)
	}

	if len(encBytes) <= modSize {
		t.Errorf("expected multi-chunk encryption (> %d bytes), got %d bytes", modSize, len(encBytes))
	}

	// Decrypt each chunk
	var result []byte
	for i := 0; i < len(encBytes); i += modSize {
		end := i + modSize
		if end > len(encBytes) {
			end = len(encBytes)
		}
		chunk, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, encBytes[i:end])
		if err != nil {
			t.Fatalf("RSA decrypt chunk at offset %d failed: %v", i, err)
		}
		result = append(result, chunk...)
	}

	if string(result) != longData {
		t.Errorf("multi-chunk round-trip mismatch: got %q, want %q", string(result), longData)
	}
}

func TestRSAEncryptEmpty(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	encrypted, err := rsaEncrypt("", privKey.PublicKey.N, privKey.PublicKey.E)
	if err != nil {
		t.Fatalf("rsaEncrypt failed: %v", err)
	}

	if encrypted != "" {
		t.Errorf("expected empty output for empty input, got %q", encrypted)
	}
}
