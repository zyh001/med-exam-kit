package server

// Web Push / VAPID 实现（纯 Go 标准库，无第三方依赖）
//
// 规范依据：
//   RFC 8292 – Voluntary Application Server Identification (VAPID)
//   RFC 8030 – Generic Event Delivery Using HTTP Push
//   RFC 8291 – Message Encryption for Web Push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ── VAPID 密钥对 ─────────────────────────────────────────────────────────

// VAPIDKeys holds the server's VAPID key pair (one per server instance).
type VAPIDKeys struct {
	PrivateKey *ecdsa.PrivateKey
	PublicKeyB64 string // URL-safe base64 uncompressed public key (for clients)
}

func generateVAPIDKeys() (*VAPIDKeys, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	pub := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	return &VAPIDKeys{
		PrivateKey:   priv,
		PublicKeyB64: base64.RawURLEncoding.EncodeToString(pub),
	}, nil
}

// vapidJWT builds a signed JWT for the Authorization: vapid header.
func vapidJWT(keys *VAPIDKeys, audience, subject string, exp time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud": audience,
		"exp": exp.Unix(),
		"sub": subject,
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	msg := header + "." + payload

	h := sha256.New()
	h.Write([]byte(msg))
	digest := h.Sum(nil)

	r, s, err := ecdsa.Sign(rand.Reader, keys.PrivateKey, digest)
	if err != nil {
		return "", err
	}
	// IEEE P1363 signature: r || s, each 32 bytes
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ── Push Subscription ─────────────────────────────────────────────────────

type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"` // client ECDH public key (base64url)
		Auth   string `json:"auth"`   // 16-byte auth secret (base64url)
	} `json:"keys"`
}

// ── Message Encryption (RFC 8291) ─────────────────────────────────────────

func encryptPushPayload(sub *PushSubscription, plaintext []byte) ([]byte, string, error) {
	// Decode client keys
	clientPubRaw, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return nil, "", fmt.Errorf("p256dh decode: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		return nil, "", fmt.Errorf("auth decode: %w", err)
	}

	// Generate ephemeral server key pair
	curve := ecdh.P256()
	serverPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	serverPub := serverPriv.PublicKey().Bytes() // uncompressed

	// ECDH: shared secret
	// Use crypto/ecdh for the actual DH
	clientECDH, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return nil, "", fmt.Errorf("ecdh client key: %w", err)
	}
	sharedBytes, err := serverPriv.ECDH(clientECDH)
	if err != nil {
		return nil, "", err
	}

	// Salt (16 random bytes)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, "", err
	}

	// PRK (RFC 8291 §3.3)
	prk := hkdfExtract(authSecret, sharedBytes)

	// key_info = "WebPush: info\x00" || clientPub || serverPub
	keyInfo := append([]byte("WebPush: info\x00"), clientPubRaw...)
	keyInfo = append(keyInfo, serverPub...)
	ikm := hkdfExpand(prk, keyInfo, 32)

	// Content encryption key + nonce
	prk2 := hkdfExtract(salt, ikm)
	cek := hkdfExpand(prk2, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk2, []byte("Content-Encoding: nonce\x00"), 12)

	// AES-128-GCM encrypt
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	// padding: add \x02 delimiter (aesgcm content coding)
	padded := append(plaintext, 0x02)
	encrypted := gcm.Seal(nil, nonce, padded, nil)

	// Build aes128gcm content (RFC 8188):
	// salt(16) || rs(4, big-endian) || idlen(1) || serverPub || ciphertext
	rs := uint32(4096)
	var body bytes.Buffer
	body.Write(salt)
	_ = binary.Write(&body, binary.BigEndian, rs)
	body.WriteByte(byte(len(serverPub)))
	body.Write(serverPub)
	body.Write(encrypted)

	return body.Bytes(), base64.RawURLEncoding.EncodeToString(serverPub), nil
}

func hkdfExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

func hkdfExpand(prk, info []byte, length int) []byte {
	var out []byte
	var prev []byte
	for i := 1; len(out) < length; i++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{byte(i)})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}
	return out[:length]
}

// ── Send a single push message ────────────────────────────────────────────

func sendPush(keys *VAPIDKeys, sub *PushSubscription, payload []byte, subject string) error {
	// Determine audience (scheme + host of endpoint)
	ep := sub.Endpoint
	parts := strings.SplitN(ep, "/", 4)
	if len(parts) < 3 {
		return fmt.Errorf("invalid endpoint: %s", ep)
	}
	audience := parts[0] + "//" + parts[2]

	jwt, err := vapidJWT(keys, audience, subject, time.Now().Add(12*time.Hour))
	if err != nil {
		return fmt.Errorf("jwt: %w", err)
	}

	body, _, err := encryptPushPayload(sub, payload)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Authorization",
		"vapid t="+jwt+",k="+keys.PublicKeyB64)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == 410 || resp.StatusCode == 404 {
		return errSubscriptionGone
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("push endpoint returned %d", resp.StatusCode)
	}
	return nil
}

var errSubscriptionGone = fmt.Errorf("subscription gone")

// ── Subscription store (in-memory, per bank) ─────────────────────────────

type pushStore struct {
	mu   sync.RWMutex
	subs map[string]*PushSubscription // endpoint → sub
}

func newPushStore() *pushStore { return &pushStore{subs: map[string]*PushSubscription{}} }

func (ps *pushStore) add(sub *PushSubscription) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.subs[sub.Endpoint] = sub
}

func (ps *pushStore) remove(endpoint string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.subs, endpoint)
}

func (ps *pushStore) all() []*PushSubscription {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]*PushSubscription, 0, len(ps.subs))
	for _, s := range ps.subs {
		out = append(out, s)
	}
	return out
}

// ── Daily push scheduler ─────────────────────────────────────────────────

// startDailyPushScheduler fires every day at the configured hour (default 08:00)
// and sends push notifications to all subscribers who have SM-2 cards due.
func (s *Server) startDailyPushScheduler() {
	go func() {
		for {
			now := time.Now()
			// Next 08:00 local time
			next := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			time.Sleep(time.Until(next))
			s.sendDailyReviewPushes()
		}
	}()
}

func (s *Server) sendDailyReviewPushes() {
	if s.vapidKeys == nil {
		return
	}
	log.Println("[push] 开始发送每日复习推送")
	sent, failed, removed := 0, 0, 0

	for _, bank := range s.cfg.Banks {
		if bank.DB == nil || s.pushStores[bank.ID] == nil {
			continue
		}
		store := s.pushStores[bank.ID]
		for _, sub := range store.all() {
			// We push without per-user due count (subscriptions are not tied to user IDs).
			// The notification encourages opening the app; the app shows the real count.
			payload, _ := json.Marshal(map[string]any{
				"title": "医考练习",
				"body":  "今日复习提醒：打开应用查看待复习题目 📚",
				"due":   0,
			})
			err := sendPush(s.vapidKeys, sub, payload, "mailto:noreply@med-exam-kit")
			if err == nil {
				sent++
			} else if err == errSubscriptionGone {
				store.remove(sub.Endpoint)
				removed++
			} else {
				log.Printf("[push] 推送失败: %v", err)
				failed++
			}
		}
	}
	log.Printf("[push] 推送完成: sent=%d failed=%d removed=%d", sent, failed, removed)
}

// vapidPublicKey returns the server's VAPID public key as URL-safe base64.
func (s *Server) vapidPublicKey() string {
	if s.vapidKeys == nil {
		return ""
	}
	return s.vapidKeys.PublicKeyB64
}

