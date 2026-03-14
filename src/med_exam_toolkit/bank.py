"""题库缓存: 加密序列化, 加速后续导出

文件格式 (MQB2):
  4 bytes  — magic b"MQB2"
  4 bytes  — meta_len (big-endian uint32)
  N bytes  — meta JSON (UTF-8)：包含 count / created / encrypted / compressed / salt_hex
  M bytes  — payload：JSON → zlib压缩 → (Fernet加密，可选)

处理顺序：
  写入：JSON 序列化 → zlib 压缩 → Fernet 加密（可选）
  读取：Fernet 解密（可选）→ zlib 解压 → JSON 反序列化

安全说明:
  - 不再使用 pickle，彻底消除反序列化代码执行风险
  - 每个题库文件生成独立随机盐值，避免彩虹表攻击

压缩说明:
  - 使用标准库 zlib，无需额外依赖
  - 压缩在加密前完成，加密数据量更小；对纯文本 JSON 通常可减小 70–80%
  - meta 中记录 compressed 标志，未压缩的旧 MQB2 文件仍可正常读取
"""
from __future__ import annotations

import base64
import dataclasses
import hashlib
import json
import os
import time
import zlib
from pathlib import Path
from typing import Any

from med_exam_toolkit.models import Question, SubQuestion

try:
    from cryptography.fernet import Fernet
    HAS_CRYPTO = True
except ImportError:
    HAS_CRYPTO = False

# MQB2 = 新的 JSON 格式；MQB1 = 旧的 pickle 格式（只读兼容）
MAGIC_V2 = b"MQB2"
MAGIC_V1 = b"MQB1"
DEFAULT_SUFFIX = ".mqb"


# ── 密钥派生 ──────────────────────────────────────────────────────────────

def _derive_key(password: str, salt: bytes) -> bytes:
    """从密码和随机盐派生 Fernet 密钥。每个文件的盐不同，防止彩虹表攻击。"""
    dk = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 100_000)
    return base64.urlsafe_b64encode(dk)


# ── JSON 序列化 / 反序列化 ────────────────────────────────────────────────

def _subq_to_dict(sq: SubQuestion) -> dict[str, Any]:
    """将 SubQuestion dataclass 转为纯 JSON 可序列化的字典。"""
    return dataclasses.asdict(sq)


def _subq_from_dict(d: dict[str, Any]) -> SubQuestion:
    """从字典重建 SubQuestion，未知字段宽容忽略（向前兼容）。"""
    known = {f.name for f in dataclasses.fields(SubQuestion)}
    return SubQuestion(**{k: v for k, v in d.items() if k in known})


def _question_to_dict(q: Question) -> dict[str, Any]:
    """将 Question dataclass 转为纯 JSON 可序列化的字典。

    raw 字段（原始 JSON）仅用于调试，序列化时主动清空，
    可将题库文件体积减小 30-60%（取决于原始数据大小）。
    """
    d = dataclasses.asdict(q)
    d["raw"] = {}   # 清空原始 JSON，不写入 MQB 文件
    # sub_questions 已由 asdict 递归处理，但为清晰起见保持显式转换
    d["sub_questions"] = [dataclasses.asdict(sq) for sq in q.sub_questions]
    return d


def _question_from_dict(d: dict[str, Any]) -> Question:
    """从字典重建 Question，sub_questions 递归还原为 SubQuestion 实例。"""
    known_q = {f.name for f in dataclasses.fields(Question)}
    kwargs = {k: v for k, v in d.items() if k in known_q}
    kwargs["sub_questions"] = [
        _subq_from_dict(sq) for sq in kwargs.get("sub_questions", [])
    ]
    return Question(**kwargs)


# ── 公开 API ──────────────────────────────────────────────────────────────

def save_bank(
    questions: list[Question],
    output: Path,
    password: str | None = None,
    compress: bool = True,
    compress_level: int = 6,
) -> Path:
    """保存题库到 .mqb 文件。

    Args:
        questions:       题目列表
        output:          输出路径（自动添加 .mqb 后缀）
        password:        加密密码，None 表示不加密
        compress:        是否启用 zlib 压缩（默认开启，通常可减小 70–80%）
        compress_level:  zlib 压缩等级 1–9，默认 6（速度与压缩率的平衡点）
    """
    fp = output.with_suffix(DEFAULT_SUFFIX)
    fp.parent.mkdir(parents=True, exist_ok=True)

    # 1. JSON 序列化（无 pickle，无代码执行风险）
    payload: bytes = json.dumps(
        [_question_to_dict(q) for q in questions],
        ensure_ascii=False,
    ).encode("utf-8")
    raw_size = len(payload)

    # 2. zlib 压缩（在加密前压缩，数据熵低，压缩效果最佳）
    if compress:
        payload = zlib.compress(payload, level=compress_level)

    # 3. 每次保存生成新的随机盐值
    salt = os.urandom(16)

    meta: dict[str, Any] = {
        "count":      len(questions),
        "created":    time.time(),
        "encrypted":  password is not None,
        "compressed": compress,
        "salt_hex":   salt.hex(),   # 盐明文存储，本身不需要保密
    }

    # 4. Fernet 加密（可选）
    if password:
        if not HAS_CRYPTO:
            raise ImportError("加密需要 cryptography 库: pip install cryptography")
        key = _derive_key(password, salt)
        payload = Fernet(key).encrypt(payload)

    meta_bytes = json.dumps(meta, ensure_ascii=False).encode("utf-8")

    with open(fp, "wb") as fh:
        fh.write(MAGIC_V2)
        fh.write(len(meta_bytes).to_bytes(4, "big"))
        fh.write(meta_bytes)
        fh.write(payload)

    compressed_size = len(payload)
    if compress:
        ratio = (1 - compressed_size / raw_size) * 100 if raw_size else 0
        import logging
        logging.getLogger(__name__).debug(
            "保存完成：原始 %d B → 压缩后 %d B（减小 %.1f%%）",
            raw_size, compressed_size, ratio,
        )

    return fp


def load_bank(path: Path, password: str | None = None) -> list[Question]:
    with open(path, "rb") as fh:
        magic = fh.read(4)
        if magic not in (MAGIC_V2, MAGIC_V1):
            raise ValueError(f"不是有效的 .mqb 文件: {path}")

        # 旧版 MQB1 文件使用 pickle，拒绝在正常加载路径中执行
        # 请使用专门的 migrate 命令进行一次性格式迁移
        if magic == MAGIC_V1:
            raise ValueError(
                f"检测到旧版 MQB1 格式 (pickle)。\n"
                f"请使用以下命令将其迁移为安全的 MQB2 格式：\n"
                f"  med-exam migrate --bank {path}\n"
                f"若已无原始 JSON，也可直接重建：\n"
                f"  med-exam build --rebuild -i <JSON目录> -o <输出路径>"
            )

        meta_len = int.from_bytes(fh.read(4), "big")
        # meta 是纯 JSON，安全反序列化
        meta: dict[str, Any] = json.loads(fh.read(meta_len).decode("utf-8"))
        payload = fh.read()

    # 1. 解密（可选）
    if meta.get("encrypted"):
        if not password:
            raise ValueError("该题库已加密，请提供 --password")
        if not HAS_CRYPTO:
            raise ImportError("解密需要 cryptography 库: pip install cryptography")
        salt = bytes.fromhex(meta["salt_hex"])
        key = _derive_key(password, salt)
        try:
            payload = Fernet(key).decrypt(payload)
        except Exception:
            raise ValueError("密码错误或文件损坏")

    # 2. 解压（兼容旧版未压缩的 MQB2 文件）
    if meta.get("compressed", False):
        payload = zlib.decompress(payload)

    # 3. JSON 反序列化（安全）
    raw_list: list[dict] = json.loads(payload.decode("utf-8"))
    return [_question_from_dict(d) for d in raw_list]


# ── 旧版 MQB1 迁移（仅供 migrate 命令调用） ──────────────────────────────

def load_bank_legacy(path: Path, password: str | None = None) -> list[Question]:
    """从旧版 MQB1 (pickle) 格式读取题库。

    ⚠️  安全警告：此函数使用 pickle 反序列化，存在代码执行风险。
    仅应由 `migrate` 命令在用户明确知情并确认的前提下调用，
    且只能用于读取用户自己生成的可信文件。
    任何面向外部输入的加载路径都不得调用此函数。

    读取完成后，调用方应立即将结果通过 save_bank() 转存为 MQB2 格式，
    此后原 MQB1 文件即可废弃。
    """
    import pickle  # 仅在此函数内导入，确保 pickle 不会被其他代码路径意外引用

    with open(path, "rb") as fh:
        magic = fh.read(4)
        if magic != MAGIC_V1:
            raise ValueError(
                f"不是有效的 MQB1 文件（magic={magic!r}）。\n"
                f"若文件是新版 MQB2 格式，请直接使用 load_bank() 加载，无需迁移。"
            )

        meta_len = int.from_bytes(fh.read(4), "big")
        # MQB1 的 meta 本身也是 pickle，在读取 magic 确认文件格式后才执行
        meta: dict = pickle.loads(fh.read(meta_len))  # noqa: S301
        payload = fh.read()

    if meta.get("encrypted"):
        if not password:
            raise ValueError("该旧版题库已加密，请通过 --password 提供原始密码")
        if not HAS_CRYPTO:
            raise ImportError("解密需要 cryptography 库: pip install cryptography")
        # MQB1 使用的是硬编码静态盐，此处必须与原始加密逻辑保持一致
        _LEGACY_SALT = b"med_exam_salt"
        dk = hashlib.pbkdf2_hmac("sha256", password.encode(), _LEGACY_SALT, 100_000)
        legacy_key = base64.urlsafe_b64encode(dk)
        try:
            payload = Fernet(legacy_key).decrypt(payload)
        except Exception:
            raise ValueError("密码错误或旧版文件损坏")

    # 用 pickle 还原题目列表；此处是唯一允许的 pickle.loads 调用点
    questions: list = pickle.loads(payload)  # noqa: S301

    # 做一次基本的类型检查，拒绝明显不符合预期的内容
    if not isinstance(questions, list):
        raise ValueError("MQB1 文件内容格式异常，无法迁移")

    return questions