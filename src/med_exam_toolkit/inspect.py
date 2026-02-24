from __future__ import annotations

import re
import unicodedata
from pathlib import Path


# ── CJK 对齐工具 ──

def _cjk_len(s: str) -> int:
    return sum(2 if unicodedata.east_asian_width(c) in ("W", "F") else 1 for c in s)


def _ljust(s: str, width: int) -> str:
    return s + " " * max(0, width - _cjk_len(s))


def _trunc(s: str, n: int = 40) -> str:
    s = (s or "").replace("\n", " ").strip()
    return s[:n] + "…" if len(s) > n else s


def _print_options(options: list[str]) -> list[str]:
    """渲染选项，避免双重字母前缀"""
    labels = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    lines = []
    for oi, opt in enumerate(options or []):
        opt = opt.strip()
        if re.match(r'^[A-Za-z][.．、]\s*', opt):
            lines.append(f"         {opt}")
        else:
            key = labels[oi] if oi < len(labels) else str(oi + 1)
            lines.append(f"         {key}. {opt}")
    return lines


# ── 主体实现 ──

def run_inspect(
    bank: str,
    password: str | None,
    filter_modes: tuple,
    filter_units: tuple,
    keyword: str,
    has_ai: bool,
    missing: bool,
    limit: int,
    full: bool,
    show_ai: bool,
) -> None:
    import click
    from med_exam_toolkit.bank import load_bank

    questions = load_bank(Path(bank), password)
    W = 72

    print_summary(questions, bank, W)

    results: list[tuple[int, int]] = []
    for qi, q in enumerate(questions):
        if filter_modes and q.mode not in filter_modes:
            continue
        if filter_units and not any(kw in (q.unit or "") for kw in filter_units):
            continue
        if keyword and keyword not in (q.stem or "") and not any(
            keyword in sq.text for sq in q.sub_questions
        ):
            continue
        for si, sq in enumerate(q.sub_questions):
            if has_ai and not (sq.ai_answer or sq.ai_discuss):
                continue
            if missing and (sq.answer or "").strip() and (sq.discuss or "").strip():
                continue
            results.append((qi, si))

    has_filter = any([filter_modes, filter_units, keyword, has_ai, missing])
    if has_filter:
        click.echo(f"\n  🔎 过滤结果：{len(results)} 个小题\n")
    else:
        n_show = "全部" if limit == 0 else str(limit)
        click.echo(f"\n  📋 题目列表（前 {n_show} 个小题）\n")

    show = results if limit == 0 else results[:limit]
    for qi, si in show:
        print_question(questions[qi], questions[qi].sub_questions[si],
                       qi, si, W, full=full, show_ai=show_ai)

    click.echo(f"{'─' * W}")
    if limit and len(results) > limit:
        click.echo(f"  … 还有 {len(results) - limit} 个，用 --limit 0 显示全部")
    click.echo()


def print_summary(questions: list, bank: str, W: int = 72) -> None:
    from collections import Counter
    import click

    total_q  = len(questions)
    total_sq = sum(len(q.sub_questions) for q in questions)
    no_ans   = sum(1 for q in questions for sq in q.sub_questions
                   if not (sq.answer or "").strip())
    no_dis   = sum(1 for q in questions for sq in q.sub_questions
                   if not (sq.discuss or "").strip())
    has_ai_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.ai_answer or sq.ai_discuss)
    ai_ans_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.answer_source == "ai")
    ai_dis_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.discuss_source == "ai")
    mode_cnt = Counter(q.mode for q in questions)
    cls_cnt  = Counter(q.cls  for q in questions)

    click.echo(f"\n{'═' * W}")
    click.echo(f"  📦 题库：{bank}")
    click.echo(f"{'─' * W}")

    lw, nw = 10, 6

    def stat_row(*pairs):
        parts = [f"{_ljust(label + '：', lw + 1)}{str(val):>{nw}}" for label, val in pairs]
        click.echo("  " + "    ".join(parts))

    stat_row(("大题数", total_q),    ("小题数", total_sq))
    stat_row(("缺答案", no_ans),     ("缺解析", no_dis))
    stat_row(("含AI内容", has_ai_c), ("AI答案兜底", ai_ans_c), ("AI解析兜底", ai_dis_c))

    click.echo(f"\n  题型分布：")
    mode_lw = max((_cjk_len(m or "未知") for m in mode_cnt), default=8)
    for m, c in sorted(mode_cnt.items()):
        click.echo(f"    {_ljust(m or '未知', mode_lw)}  {c:>6} 题")

    if len(cls_cnt) > 1:
        click.echo(f"\n  分类分布：")
        cls_lw = max((_cjk_len(c or "未知") for c in cls_cnt), default=10)
        for c, n in sorted(cls_cnt.items(), key=lambda x: -x[1])[:10]:
            click.echo(f"    {_ljust(c or '未知', cls_lw)}  {n:>6} 题")

    click.echo(f"{'═' * W}")


def print_question(q, sq, qi: int, si: int, W: int = 72,
                   *, full: bool, show_ai: bool) -> None:
    import click

    ans_flag = ("✅" if (sq.answer  or "").strip()
                else "🤖" if (sq.ai_answer or "").strip() else "❓")
    dis_flag = ("✅" if (sq.discuss or "").strip()
                else "🤖" if (sq.ai_discuss or "").strip() else "❓")
    ai_tag   = " [AI]" if (sq.ai_answer or sq.ai_discuss) else ""

    click.echo(f"{'─' * W}")
    click.echo(
        f"  [{qi+1}-{si+1}]  {q.mode}  {q.unit or ''}  "
        f"答案:{ans_flag}  解析:{dis_flag}{ai_tag}"
    )

    if q.stem:
        click.echo(f"  【题干】{q.stem if full else _trunc(q.stem, 100)}")

    text = sq.text or "(无题目文本)"
    click.echo(f"  【题目】{text if full else _trunc(text, 100)}")

    for line in _print_options(sq.options):
        click.echo(line)

    eff_ans = sq.eff_answer
    if eff_ans:
        src  = " (AI)" if sq.answer_source == "ai" else ""
        conf = (f"  [置信:{sq.ai_confidence:.2f}  模型:{sq.ai_model}]"
                if sq.answer_source == "ai" else "")
        click.echo(f"  【答案】{eff_ans}{src}{conf}")

    eff_dis = sq.eff_discuss
    if eff_dis:
        src     = " (AI)" if sq.discuss_source == "ai" else ""
        dis_out = eff_dis if full else _trunc(eff_dis, 150)
        click.echo(f"  【解析】{dis_out}{src}")

    if show_ai and (sq.ai_answer or sq.ai_discuss):
        sep = "─" * ((W - 12) // 2)
        click.echo(f"  {sep} AI原始输出 {sep}")
        if sq.ai_answer and sq.ai_answer != sq.eff_answer:
            click.echo(f"  【AI答案】{sq.ai_answer}"
                       f"  [置信:{sq.ai_confidence:.2f}  模型:{sq.ai_model}]")
        if sq.ai_discuss:
            ai_dis = sq.ai_discuss if full else _trunc(sq.ai_discuss, 150)
            same   = sq.ai_discuss.strip() == (sq.discuss or "").strip()
            note   = "（与官方解析相同）" if same else ""
            click.echo(f"  【AI解析】{ai_dis}{note}")