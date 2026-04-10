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


def build_ai_chat_prompt(
    q: Question,
    sq_idx: int,
    user_answer: str,
) -> list[dict[str, str]]:
    """为 AI 答疑面板构建 system + 初始 user 消息。"""
    sq = q.sub_questions[sq_idx]

    system_prompt = (
        "你是一位资深的医学考试辅导老师，擅长帮助学生深入理解题目背后的知识点，"
        "学生有两次向你提问的机会。\n\n"
        "你的任务：\n"
        "1. 分析题目的关键考点\n"
        "2. 逐项分析每个选项，说明为什么对或错\n"
        "3. 给出最终结论和推理过程\n\n"
        "要求：\n"
        "- 全部使用中文回答，包括思考过程(必要的名词可以使用英文表述)\n"
        "- 语言简洁清晰，重点突出\n"
        "- 使用医学术语要准确\n"
        "- 如果学生选错了，要特别指出其思路中可能的误区\n"
        "- 回答格式：考点分析 → 选项逐项解析 → 最终结论"
    )

    parts: list[str] = []
    mode = q.mode or "未知"
    parts.append(f"题型: {mode}")
    if q.stem:
        parts.append(f"题干: {q.stem}")
    if sq.text:
        parts.append(f"小题: {sq.text}")

    eff_opts = sq.options or q.shared_options
    if eff_opts:
        parts.append("选项:")
        parts.append(_format_options(eff_opts))

    correct = sq.eff_answer
    if correct:
        parts.append(f"正确答案: {correct}")

    if user_answer:
        parts.append(f"我的选择: {user_answer}")
        if correct and user_answer != correct:
            parts.append("（我选错了）")

    if sq.error_prone:
        parts.append(f"易错点: {sq.error_prone}")
    if sq.point:
        parts.append(f"知识点: {sq.point}")

    return [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": "\n".join(parts)},
    ]
