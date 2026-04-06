"""
Web Push / VAPID 实现（Python，依赖 cryptography + PyJWT）

规范：
  RFC 8292 – VAPID
  RFC 8030 – Generic Event Delivery Using HTTP Push
  RFC 8291 – Message Encryption for Web Push
"""
from __future__ import annotations

import base64
import json
import logging
import os
import struct
import threading
import time
from dataclasses import dataclass, field
from typing import Optional

import jwt as pyjwt
import requests
from cryptography.hazmat.primitives.asymmetric.ec import (
    ECDH, SECP256R1, generate_private_key, EllipticCurvePublicKey,
)
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.hashes import SHA256
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

log = logging.getLogger(__name__)


# ── VAPID 密钥对 ──────────────────────────────────────────────────────────

@dataclass
class VAPIDKeys:
    private_key: object          # EllipticCurvePrivateKey
    public_key_b64: str          # URL-safe base64 uncompressed public key


def generate_vapid_keys() -> VAPIDKeys:
    priv = generate_private_key(SECP256R1())
    pub  = priv.public_key().public_bytes(
        serialization.Encoding.X962,
        serialization.PublicFormat.UncompressedPoint,
    )
    return VAPIDKeys(
        private_key=priv,
        public_key_b64=base64.urlsafe_b64encode(pub).rstrip(b"=").decode(),
    )


def _vapid_jwt(keys: VAPIDKeys, audience: str, subject: str, exp: int) -> str:
    """Build a signed VAPID JWT."""
    priv_pem = keys.private_key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.TraditionalOpenSSL,
        serialization.NoEncryption(),
    )
    return pyjwt.encode(
        {"aud": audience, "exp": exp, "sub": subject},
        priv_pem,
        algorithm="ES256",
    )


# ── Message Encryption (RFC 8291) ─────────────────────────────────────────

def _hkdf(salt: bytes, ikm: bytes, info: bytes, length: int) -> bytes:
    return HKDF(SHA256(), length, salt, info).derive(ikm)


def encrypt_push_payload(sub: "PushSubscription", plaintext: bytes) -> bytes:
    """Encrypt plaintext for the given push subscription (aes128gcm)."""
    # Decode client keys
    p256dh = base64.urlsafe_b64decode(_pad_b64(sub.keys_p256dh))
    auth   = base64.urlsafe_b64decode(_pad_b64(sub.keys_auth))

    # Ephemeral server key pair
    server_priv = generate_private_key(SECP256R1())
    server_pub  = server_priv.public_key().public_bytes(
        serialization.Encoding.X962,
        serialization.PublicFormat.UncompressedPoint,
    )

    # Load client public key and perform ECDH
    from cryptography.hazmat.primitives.asymmetric.ec import EllipticCurvePublicKey
    from cryptography.hazmat.backends import default_backend
    client_pub = EllipticCurvePublicKey.from_encoded_point(SECP256R1(), p256dh)
    shared_secret = server_priv.exchange(ECDH(), client_pub)

    # Salt
    salt = os.urandom(16)

    # PRK (RFC 8291 §3.3)
    prk = _hkdf(auth, shared_secret,
                b"WebPush: info\x00" + p256dh + server_pub, 32)

    # Content encryption key + nonce
    cek   = _hkdf(salt, prk, b"Content-Encoding: aes128gcm\x00", 16)
    nonce = _hkdf(salt, prk, b"Content-Encoding: nonce\x00",     12)

    # AES-128-GCM encrypt (add \x02 delimiter)
    ciphertext = AESGCM(cek).encrypt(nonce, plaintext + b"\x02", b"")

    # Build aes128gcm content (RFC 8188):
    # salt(16) | rs(4 BE) | idlen(1) | serverPub(65) | ciphertext
    header = salt + struct.pack(">I", 4096) + bytes([len(server_pub)]) + server_pub
    return header + ciphertext


def _pad_b64(s: str) -> str:
    return s + "=" * (-len(s) % 4)


# ── Send a push message ───────────────────────────────────────────────────

class SubscriptionGone(Exception):
    pass


def send_push(keys: VAPIDKeys, sub: "PushSubscription",
              payload: bytes, subject: str = "mailto:noreply@med-exam-kit") -> None:
    ep = sub.endpoint
    audience = "/".join(ep.split("/")[:3])

    exp = int(time.time()) + 12 * 3600
    token = _vapid_jwt(keys, audience, subject, exp)

    body = encrypt_push_payload(sub, payload)

    resp = requests.post(
        ep,
        data=body,
        headers={
            "Content-Type":     "application/octet-stream",
            "Content-Encoding": "aes128gcm",
            "TTL":              "86400",
            "Authorization":    f"vapid t={token},k={keys.public_key_b64}",
        },
        timeout=10,
    )

    if resp.status_code in (404, 410):
        raise SubscriptionGone(ep)
    if resp.status_code >= 400:
        raise RuntimeError(f"push endpoint returned {resp.status_code}")


# ── Subscription dataclass ────────────────────────────────────────────────

@dataclass
class PushSubscription:
    endpoint:    str
    keys_p256dh: str
    keys_auth:   str

    @classmethod
    def from_dict(cls, d: dict) -> "PushSubscription":
        keys = d.get("keys", {})
        return cls(
            endpoint=d["endpoint"],
            keys_p256dh=keys["p256dh"],
            keys_auth=keys["auth"],
        )


@dataclass
class PushEntry:
    sub: PushSubscription
    uid: str = ""   # 用户 ID（med_exam_uid），可为空


# ── In-memory subscription store ─────────────────────────────────────────

class PushStore:
    def __init__(self) -> None:
        self._lock  = threading.Lock()
        self._by_ep:  dict[str, PushEntry] = {}   # endpoint → entry
        self._by_uid: dict[str, PushEntry] = {}   # uid → entry

    def add(self, sub: PushSubscription, uid: str = "") -> None:
        with self._lock:
            entry = PushEntry(sub=sub, uid=uid)
            # 同一 uid 的旧订阅先清理
            if uid and uid in self._by_uid:
                old_ep = self._by_uid[uid].sub.endpoint
                if old_ep != sub.endpoint:
                    self._by_ep.pop(old_ep, None)
            if uid:
                self._by_uid[uid] = entry
            self._by_ep[sub.endpoint] = entry

    def remove(self, endpoint: str) -> None:
        with self._lock:
            entry = self._by_ep.pop(endpoint, None)
            if entry and entry.uid:
                self._by_uid.pop(entry.uid, None)

    def all(self) -> list[PushSubscription]:
        with self._lock:
            return [e.sub for e in self._by_ep.values()]

    def for_uid(self, uid: str) -> Optional[PushSubscription]:
        """按用户 ID 查询订阅，找不到返回 None。"""
        with self._lock:
            entry = self._by_uid.get(uid)
            return entry.sub if entry else None


# ── Daily push scheduler ──────────────────────────────────────────────────

def start_daily_push_scheduler(keys: VAPIDKeys, store: PushStore) -> None:
    """Start a background thread that fires every day at 08:00."""
    def _loop() -> None:
        while True:
            now  = time.localtime()
            # Seconds until next 08:00
            secs_since_midnight = now.tm_hour * 3600 + now.tm_min * 60 + now.tm_sec
            target = 8 * 3600
            wait   = (target - secs_since_midnight) % 86400 or 86400
            time.sleep(wait)
            _send_daily(keys, store)

    t = threading.Thread(target=_loop, daemon=True, name="push-scheduler")
    t.start()
    log.info("[push] 每日复习提醒调度器已启动（每天 08:00）")


def _send_daily(keys: VAPIDKeys, store: PushStore) -> None:
    log.info("[push] 开始发送每日复习推送")
    subs = store.all()
    sent = failed = removed = 0

    payload = json.dumps({
        "title": "医考练习",
        "body":  "今日复习提醒：打开应用查看待复习题目 📚",
        "due":   0,
    }).encode()

    for sub in subs:
        try:
            send_push(keys, sub, payload)
            sent += 1
        except SubscriptionGone:
            store.remove(sub.endpoint)
            removed += 1
        except Exception as e:
            log.warning("[push] 推送失败: %s", e)
            failed += 1

    log.info("[push] 推送完成: sent=%d failed=%d removed=%d", sent, failed, removed)
