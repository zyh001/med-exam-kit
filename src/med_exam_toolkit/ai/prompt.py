"""Prompt 构建模块（按小题补答案/解析）"""
from __future__ import annotations

import textwrap
from med_exam_toolkit.models import Question, SubQuestion


def _format_options(options: list[str]) -> str:
    labels = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    lines: list[str] = []
    for i, opt in enumerate(options or []):
        key = labels[i] if i < len(labels) else str(i + 1)
        lines.append(f"{key}. {opt}")
    return "\n".join(lines)


def build_subquestion_prompt(
    q: Question,
    sq: SubQuestion,
    *,
    need_answer: bool,
    need_discuss: bool,
) -> str:
    known_answer = (sq.answer or "").strip()
    options_text = _format_options(sq.options)

    task = "请补全答案和解析" if need_answer else "请仅补全解析（不要改答案）"

    answer_rule = (
        '必须输出 "answer" 字段，值为选项字母（如 A/B/C/D 或多选 AC）'
        if need_answer
        else f'已知正确答案为 "{known_answer}"，不要改动答案，仅输出 discuss'
    )

    output_schema = (
        '{ "answer": "A", "discuss": "...", "confidence": 0.0 }'
        if need_answer
        else '{ "discuss": "...", "confidence": 0.0 }'
    )

    return textwrap.dedent(
        f"""\
        你是医学考试辅导专家。{task}，并且仅返回 JSON。

        输出格式:
        {output_schema}

        规则:
        1) {answer_rule}
        2) discuss 要简洁、医学上准确，要有理有据的说明为何正确并简要排除干扰项
        3) confidence 为 0~1 小数
        4) 禁止输出 markdown、代码块或多余文本

        题目信息:
        题型: {q.mode or "未知"}
        章节: {q.unit or "未知"}
        题干: {q.stem or ""}
        小题: {sq.text or ""}
        选项:
        {options_text}
        已知答案: {known_answer or "无"}
        """
    ).strip()
