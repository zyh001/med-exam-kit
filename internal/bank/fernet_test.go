package bank

import (
	"bytes"
	"testing"
)

func TestFernet_RoundTrip(t *testing.T) {
	salt := []byte("testsalt01234567")
	key := DeriveKey("mypassword", salt)

	plain := []byte("hello 你好 world")
	token, err := FernetEncrypt(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := FernetDecrypt(key, token)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip failed: want %q, got %q", plain, got)
	}
}

func TestFernet_WrongKeyFails(t *testing.T) {
	salt := []byte("testsalt01234567")
	key1 := DeriveKey("password1", salt)
	key2 := DeriveKey("password2", salt)

	token, _ := FernetEncrypt(key1, []byte("secret"))
	if _, err := FernetDecrypt(key2, token); err == nil {
		t.Fatal("wrong key should have failed")
	}
}

func TestFernet_TamperedTokenFails(t *testing.T) {
	salt := []byte("testsalt01234567")
	key := DeriveKey("password", salt)
	token, _ := FernetEncrypt(key, []byte("secret"))

	// Flip a byte in the middle
	token[len(token)/2] ^= 0xFF
	if _, err := FernetDecrypt(key, token); err == nil {
		t.Fatal("tampered token should have failed")
	}
}

func TestPBKDF2_Deterministic(t *testing.T) {
	salt := []byte("salt1234")
	k1 := DeriveKey("pass", salt)
	k2 := DeriveKey("pass", salt)
	if !bytes.Equal(k1, k2) {
		t.Fatal("PBKDF2 is not deterministic")
	}
}

func TestPBKDF2_SaltMatters(t *testing.T) {
	k1 := DeriveKey("pass", []byte("salt1"))
	k2 := DeriveKey("pass", []byte("salt2"))
	if bytes.Equal(k1, k2) {
		t.Fatal("different salts should produce different keys")
	}
}
