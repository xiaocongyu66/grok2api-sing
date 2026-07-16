package security

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("refresh-secret")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "refresh-secret" {
		t.Fatal("密文不应等于明文")
	}
	plain, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "refresh-secret" {
		t.Fatalf("解密结果 = %q", plain)
	}
}

func TestCipherAADBindsContextAndFallsBackToLegacy(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cipher.Encrypt("legacy-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Legacy rows sealed without AAD remain readable with AAD decrypt.
	plain, err := cipher.DecryptAAD(legacy, ClientKeyAAD("ab12cd"))
	if err != nil || plain != "legacy-secret" {
		t.Fatalf("legacy decrypt = %q err=%v", plain, err)
	}
	bound, err := cipher.EncryptAAD("bound-secret", ClientKeyAAD("ab12cd"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cipher.DecryptAAD(bound, ClientKeyAAD("wrong")); err == nil {
		t.Fatal("wrong AAD accepted")
	}
	plain, err = cipher.DecryptAAD(bound, ClientKeyAAD("ab12cd"))
	if err != nil || plain != "bound-secret" {
		t.Fatalf("aad decrypt = %q err=%v", plain, err)
	}
}
