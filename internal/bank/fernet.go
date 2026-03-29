package bank

// Fernet token format (big-endian, before base64url encoding):
//   0x80          version     1 byte
//   uint64        timestamp   8 bytes
//   [16]byte      IV          16 bytes
//   []byte        ciphertext  N*16 bytes  (AES-128-CBC, PKCS7)
//   [32]byte      HMAC        32 bytes    (SHA256 of version..ciphertext)
//
// Fernet key (32 raw bytes, base64url-encoded to 44 chars):
//   key[:16]  signing key   (HMAC-SHA256)
//   key[16:]  encryption key (AES-128-CBC)
//
// Key derivation (matches Python bank.py _derive_key):
//   dk = PBKDF2-HMAC-SHA256(password, salt, 100_000 iterations, 32 bytes)
//   fernet_key = base64url(dk)   <- 44-char string

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// DeriveKey reproduces Python's _derive_key: PBKDF2-HMAC-SHA256 → base64url.
func DeriveKey(password string, salt []byte) []byte {
	dk := pbkdf2Key([]byte(password), salt, 100_000, 32)
	out := make([]byte, base64.RawURLEncoding.EncodedLen(len(dk)))
	base64.RawURLEncoding.Encode(out, dk)
	return out // 43-byte key string (RawURL: no padding)
}

// FernetEncrypt encrypts plaintext using a derived key (result of DeriveKey).
func FernetEncrypt(key, plaintext []byte) ([]byte, error) {
	rawKey, err := base64.RawURLEncoding.DecodeString(string(key))
	if err != nil {
		// try with standard padding
		rawKey, err = base64.URLEncoding.DecodeString(string(key))
		if err != nil {
			return nil, err
		}
	}
	if len(rawKey) != 32 {
		return nil, errors.New("fernet: key must be 32 bytes")
	}
	signingKey, encKey := rawKey[:16], rawKey[16:]

	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}

	ciphertext, err := aesCBCEncrypt(encKey, iv, plaintext)
	if err != nil {
		return nil, err
	}

	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(time.Now().Unix()))

	// header = version(1) + timestamp(8) + iv(16)
	header := append([]byte{0x80}, ts...)
	header = append(header, iv...)
	payload := append(header, ciphertext...)

	mac := hmacSHA256(signingKey, payload)
	token := append(payload, mac...)
	return []byte(base64.URLEncoding.EncodeToString(token)), nil
}

// FernetDecrypt decrypts a Fernet token using a derived key.
func FernetDecrypt(key, token []byte) ([]byte, error) {
	rawKey, err := base64.RawURLEncoding.DecodeString(string(key))
	if err != nil {
		rawKey, err = base64.URLEncoding.DecodeString(string(key))
		if err != nil {
			return nil, err
		}
	}
	if len(rawKey) != 32 {
		return nil, errors.New("fernet: key must be 32 bytes")
	}
	signingKey, encKey := rawKey[:16], rawKey[16:]

	data, err := base64.URLEncoding.DecodeString(string(token))
	if err != nil {
		return nil, errors.New("fernet: invalid base64")
	}
	// minimum: 1 + 8 + 16 + 16 + 32 = 73 bytes
	if len(data) < 73 {
		return nil, errors.New("fernet: token too short")
	}
	if data[0] != 0x80 {
		return nil, errors.New("fernet: unknown version")
	}

	macStart := len(data) - 32
	mac := data[macStart:]
	payload := data[:macStart]

	expected := hmacSHA256(signingKey, payload)
	if !hmac.Equal(mac, expected) {
		return nil, errors.New("fernet: signature mismatch — wrong password or corrupted file")
	}

	iv := data[9:25]
	ciphertext := data[25:macStart]
	return aesCBCDecrypt(encKey, iv, ciphertext)
}

// ── internal helpers ───────────────────────────────────────────────────

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func aesCBCEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	// PKCS7 pad
	padLen := 16 - len(plaintext)%16
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return ciphertext, nil
}

func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	if len(ciphertext)%16 != 0 {
		return nil, errors.New("fernet: ciphertext not aligned to block size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	// Remove PKCS7 padding
	if len(plaintext) == 0 {
		return nil, errors.New("fernet: empty plaintext after decrypt")
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen == 0 || padLen > 16 {
		return nil, errors.New("fernet: invalid padding")
	}
	return plaintext[:len(plaintext)-padLen], nil
}

// pbkdf2Key is a minimal PBKDF2-HMAC-SHA256 (avoids golang.org/x/crypto dependency).
func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size // 32
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)

	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		binary.BigEndian.PutUint32(buf, uint32(block))

		// U1 = PRF(password, salt || INT(block))
		h := hmac.New(sha256.New, password)
		h.Write(salt)
		h.Write(buf)
		u := h.Sum(nil)
		t := append([]byte(nil), u...)

		for n := 1; n < iter; n++ {
			h.Reset()
			h.Write(u)
			u = h.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}
