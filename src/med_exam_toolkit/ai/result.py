"""AI 结果解析与验证（答案/解析补全）"""
from __future__ import annotations

import json
import logging
import re
from typing import Any

from med_exam_toolkit.models import SubQuestion

logger = logging.getLogger(__name__)


def parse_response(raw: str) -> dict[str, Any]:
    text = (raw or "").strip()
    if not text:
        return {}

    # 策略 1：直接 JSON
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass

    # 策略 2：markdown 代码块
    m = re.search(r"```(?:json)?\s*(\{.*?\})\s*```", text, re.DOTALL)
    if m:
        try:
            return json.loads(m.group(1))
        except json.JSONDecodeError:
            pass

    # 策略 3：提取首个 JSON 对象
    m = re.search(r"\{.*\}", text, re.DOTALL)
    if m:
        try:
            return json.loads(m.group(0))
        except json.JSONDecodeError:
            pass

    logger.warning("AI 返回内容无法解析为 JSON: %s", text[:200])
    return {}


def validate_result(
    result: dict[str, Any],
    *,
    need_answer: bool,
    need_discuss: bool,
) -> tuple[bool, list[str]]:
    missing: list[str] = []
    if need_answer and not str(result.get("answer", "")).strip():
        missing.append("answer")
    if need_discuss and not str(result.get("discuss", "")).strip():
        missing.append("discuss")
    return (len(missing) == 0), missing


def apply_to_subquestion(
    sq: SubQuestion,
    result: dict[str, Any],
    *,
    model_name: str,
    overwrite: bool = False,
) -> None:
    if not result:
        return

    ai_answer = str(result.get("answer", "")).strip()
    ai_discuss = str(result.get("discuss", "")).strip()

    try:
        ai_confidence = float(result.get("confidence", 0.0) or 0.0)
    except (TypeError, ValueError):
        ai_confidence = 0.0

    if ai_answer:
        sq.ai_answer = ai_answer
    if ai_discuss:
        sq.ai_discuss = ai_discuss
    sq.ai_confidence = ai_confidence
    sq.ai_model = model_name
    sq.ai_status = "pending"

    if overwrite:
        if ai_answer and not (sq.answer or "").strip():
            sq.answer = ai_answer
        if ai_discuss and not (sq.discuss or "").strip():
            sq.discuss = ai_discuss
