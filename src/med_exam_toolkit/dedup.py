from __future__ import annotations
import hashlib
import re
from med_exam_toolkit.models import Question


def _normalize_text(text: str) -> str:
    """去除空白、标点差异，统一用于指纹计算"""
    text = re.sub(r"\s+", "", text)
    # 统一中英文标点
    text = text.replace("，", ",").replace("。", ".").replace("；", ";")
    text = text.replace("：", ":").replace("（", "(").replace("）", ")")
    return text.lower()

def _resolve_answer_text(sq) -> str:
    """将答案字母转为实际选项文本，避免选项顺序影响指纹"""
    try:
        idx = ord(sq.answer.strip().upper()) - ord("A")
        if 0 <= idx < len(sq.options):
            return _normalize_text(sq.options[idx])
    except (TypeError, ValueError):
        pass
    return sq.answer.strip().upper()

def compute_fingerprint(q: Question, strategy: str = "strict") -> str:
    """
    计算题目指纹。

    strategy:
        - content: 仅基于题干/子题文本
        - strict:  题干 + 选项(排序) + 答案文本 + 共享选项(B型题)
    """
    parts: list[str] = []

    # 共享题干
    if q.stem:
        parts.append(_normalize_text(q.stem))

    # B型题的共享选项（必须参与指纹计算，避免错误去重）
    if q.shared_options and strategy == "strict":
        sorted_shared = sorted(_normalize_text(opt) for opt in q.shared_options)
        parts.extend(sorted_shared)

    for sq in q.sub_questions:
        parts.append(_normalize_text(sq.text))
        if strategy == "strict":
            # 排序选项，消除顺序差异
            sorted_opts = sorted(_normalize_text(opt) for opt in sq.options)
            parts.extend(sorted_opts)
            # 用答案文本而非字母
            parts.append(_resolve_answer_text(sq))

    raw_str = "|".join(parts)
    return hashlib.sha256(raw_str.encode("utf-8")).hexdigest()[:16]


def deduplicate(
    questions: list[Question],
    strategy: str = "strict",
) -> list[Question]:
    """
    去重，返回去重后的列表。
    保留首次出现的题目，后续重复的丢弃。
    """
    seen: dict[str, Question] = {}
    duplicates = 0

    for q in questions:
        fp = compute_fingerprint(q, strategy)
        q.fingerprint = fp
        if fp in seen:
            duplicates += 1
            # 合并来源信息
            existing = seen[fp]
            if q.pkg not in existing.pkg:
                existing.pkg += f",{q.pkg}"
        else:
            seen[fp] = q

    result = list(seen.values())
    print(f"[INFO] 去重完成: {len(questions)} -> {len(result)} (去除 {duplicates} 条重复)")
    return result