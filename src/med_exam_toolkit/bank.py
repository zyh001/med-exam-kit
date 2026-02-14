"""题库缓存: 加密序列化, 加速后续导出"""
from __future__ import annotations
import pickle
import hashlib
import time
from pathlib import Path
from med_exam_toolkit.models import Question

try:
    from cryptography.fernet import Fernet
    HAS_CRYPTO = True
except ImportError:
    HAS_CRYPTO = False

MAGIC = b"MQB1"
DEFAULT_SUFFIX = ".mqb"


def _derive_key(password: str) -> bytes:
    import base64
    dk = hashlib.pbkdf2_hmac("sha256", password.encode(), b"med_exam_salt", 100_000)
    return base64.urlsafe_b64encode(dk)


def save_bank(
    questions: list[Question],
    output: Path,
    password: str | None = None,
) -> Path:
    fp = output.with_suffix(DEFAULT_SUFFIX)
    fp.parent.mkdir(parents=True, exist_ok=True)

    payload = pickle.dumps(questions, protocol=pickle.HIGHEST_PROTOCOL)

    meta = {
        "count": len(questions),
        "created": time.time(),
        "encrypted": password is not None,
    }
    meta_bytes = pickle.dumps(meta)

    if password:
        if not HAS_CRYPTO:
            raise ImportError("加密需要 cryptography 库: pip install cryptography")
        key = _derive_key(password)
        f = Fernet(key)
        payload = f.encrypt(payload)

    with open(fp, "wb") as fh:
        fh.write(MAGIC)
        fh.write(len(meta_bytes).to_bytes(4, "big"))
        fh.write(meta_bytes)
        fh.write(payload)

    return fp


def load_bank(path: Path, password: str | None = None) -> list[Question]:
    with open(path, "rb") as fh:
        magic = fh.read(4)
        if magic != MAGIC:
            raise ValueError(f"不是有效的 .mqb 文件: {path}")

        meta_len = int.from_bytes(fh.read(4), "big")
        meta_bytes = fh.read(meta_len)
        meta = pickle.loads(meta_bytes)

        payload = fh.read()

    if meta.get("encrypted"):
        if not password:
            raise ValueError("该题库已加密，请提供 --password")
        if not HAS_CRYPTO:
            raise ImportError("解密需要 cryptography 库: pip install cryptography")
        key = _derive_key(password)
        f = Fernet(key)
        try:
            payload = f.decrypt(payload)
        except Exception:
            raise ValueError("密码错误或文件损坏")

    questions = pickle.loads(payload)
    return questions
