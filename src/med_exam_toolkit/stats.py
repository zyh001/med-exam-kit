"""题目统计分析"""
from __future__ import annotations
from collections import Counter
from med_exam_toolkit.models import Question
import unicodedata

DIFFICULTY_LABELS = {
    "easy": "简单 (≥80%)",
    "medium": "中等 (60-80%)",
    "hard": "较难 (40-60%)",
    "extreme": "困难 (<40%)",
    "unknown": "未知 (无正确率)",
}

DIFFICULTY_ORDER = ["easy", "medium", "hard", "extreme", "unknown"]

def _parse_rate(raw: str) -> float | None:
    if not raw or not raw.strip():
        return None
    s = raw.strip().rstrip("%")
    try:
        v = float(s)
        return v if 0 <= v <= 100 else None
    except ValueError:
        return None

def _display_width(s: str) -> int:
    """计算字符串在终端的显示宽度"""
    return sum(2 if unicodedata.east_asian_width(c) in ("F", "W") else 1 for c in s)

def _pad_right(s: str, width: int) -> str:
    """按显示宽度右补空格"""
    return s + " " * (width - _display_width(s))

def _classify_difficulty(q: Question) -> str:
    rates = []
    for sq in q.sub_questions:
        r = _parse_rate(sq.rate)
        if r is not None:
            rates.append(r)
    if not rates:
        return "unknown"
    avg = sum(rates) / len(rates)
    if avg >= 80:
        return "easy"
    if avg >= 60:
        return "medium"
    if avg >= 40:
        return "hard"
    return "extreme"


def summarize(questions: list[Question], full: bool = False) -> dict:
    """生成统计摘要, full=True 时章节/题库不截断"""
    by_mode = Counter()
    by_unit = Counter()
    by_pkg = Counter()
    by_cls = Counter()
    by_difficulty = Counter()
    low_rate_questions = []
    total_subquestions = 0

    for q in questions:
        by_mode[q.mode] += 1
        by_unit[q.unit] += 1
        by_pkg[q.pkg] += 1
        by_cls[q.cls] += 1
        by_difficulty[_classify_difficulty(q)] += 1
        total_subquestions += len(q.sub_questions)

        for sq in q.sub_questions:
            if sq.rate:
                try:
                    rate_val = float(sq.rate.replace("%", "").strip())
                    if rate_val < 50:
                        low_rate_questions.append({
                            "text": sq.text[:60],
                            "rate": sq.rate,
                            "answer": sq.answer,
                            "unit": q.unit,
                            "mode": q.mode,
                        })
                except ValueError:
                    pass

    unit_limit = None if full else 20

    # 按 DIFFICULTY_ORDER 排序
    difficulty_sorted = {
        k: by_difficulty.get(k, 0)
        for k in DIFFICULTY_ORDER
        if by_difficulty.get(k, 0) > 0
    }

    return {
        "total": len(questions),
        "total_subquestions": total_subquestions,
        "by_mode": dict(by_mode.most_common()),
        "by_unit": dict(by_unit.most_common(unit_limit)),
        "by_pkg": dict(by_pkg.most_common()),
        "by_cls": dict(by_cls.most_common()),
        "by_difficulty": difficulty_sorted,
        "unit_total": len(by_unit),
        "low_rate_count": len(low_rate_questions),
        "low_rate_top10": sorted(
            low_rate_questions,
            key=lambda x: float(x["rate"].replace("%", "").strip()),
        )[:10],
        "full": full,
    }


def print_summary(questions: list[Question], full: bool = False) -> None:
    """打印统计摘要到终端"""
    s = summarize(questions, full=full)
    total = s["total"] or 1
    print(f"\n{'='*50}")
    print(f"📊 题目统计")
    print(f"{'='*50}")
    print(f"总题数: {s['total']} 道大题, {s['total_subquestions']} 道小题")

    def _print_section(title: str, data: dict, show_bar: bool = True, show_pct: bool = True):
        print(f"\n{title}:")
        if not data:
            print("  (无数据)")
            return
        # 自动计算标签列宽度
        labels = {k: (k if k.strip() else "未知") for k in data}
        col_width = max(_display_width(v) for v in labels.values()) + 2
        max_count = max(data.values())
        for key, count in data.items():
            label = labels[key]
            padded = _pad_right(label, col_width)
            pct =  f"({count / total * 100:>5.1f}%)" if show_pct else ""
            bar = " " + "■" * round(count / max_count * 20) if show_bar else ""
            print(f"  {padded} {count:>5d} {pct}{bar}")

    _print_section("按题型", s["by_mode"])

    difficulty_labeled = {
        DIFFICULTY_LABELS.get(k, k): v for k, v in s["by_difficulty"].items()
    }
    _print_section("按难度", difficulty_labeled)

    _print_section("按来源", s["by_pkg"])
    _print_section("按题库", s["by_cls"], show_bar=False)

    unit_items = list(s["by_unit"].items())
    if full:
        _print_section(f"按章节 (共 {s['unit_total']} 个)", s["by_unit"], show_bar=False, show_pct=False)
    else:
        top10 = dict(unit_items[:10])
        _print_section(f"按章节 (Top 10 / 共 {s['unit_total']} 个)", top10, show_bar=False, show_pct=False)
        if s["unit_total"] > 10:
            print(f"  ... 还有 {s['unit_total'] - 10} 个章节")

    if s["low_rate_count"]:
        print(f"\n⚠️  正确率 < 50% 的题目: {s['low_rate_count']} 道")
        for item in s["low_rate_top10"]:
            print(f"  [{item['rate']}] {item['text']}...")

    print(f"{'='*50}\n")
