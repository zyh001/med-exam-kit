"""题目统计分析"""
from __future__ import annotations
from collections import Counter
from med_exam_toolkit.models import Question

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

    for q in questions:
        by_mode[q.mode] += 1
        by_unit[q.unit] += 1
        by_pkg[q.pkg] += 1
        by_cls[q.cls] += 1
        by_difficulty[_classify_difficulty(q)] += 1

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
    print(f"\n{'='*50}")
    print(f"📊 题目统计")
    print(f"{'='*50}")
    print(f"总题数: {s['total']}")

    print(f"\n按题型:")
    for mode, count in s["by_mode"].items():
        print(f"  {mode}: {count}")

    print(f"\n按难度:")
    for level, count in s["by_difficulty"].items():
        label = DIFFICULTY_LABELS.get(level, level)
        pct = count / s["total"] * 100 if s["total"] else 0
        bar = "█" * int(pct / 2)
        print(f"  {label}: {count} ({pct:.1f}%) {bar}")

    print(f"\n按来源:")
    for pkg, count in s["by_pkg"].items():
        print(f"  {pkg}: {count}")

    print(f"\n按题库:")
    for cls, count in s["by_cls"].items():
        print(f"  {cls}: {count}")

    unit_items = list(s["by_unit"].items())
    if full:
        print(f"\n按章节 (共 {s['unit_total']} 个):")
        for unit, count in unit_items:
            print(f"  {unit}: {count}")
    else:
        print(f"\n按章节 (Top 10 / 共 {s['unit_total']} 个):")
        for unit, count in unit_items[:10]:
            print(f"  {unit}: {count}")
        if s["unit_total"] > 10:
            print(f"  ... 还有 {s['unit_total'] - 10} 个章节")

    if s["low_rate_count"]:
        print(f"\n⚠️  正确率 < 50% 的题目: {s['low_rate_count']} 道")
        for item in s["low_rate_top10"]:
            print(f"  [{item['rate']}] {item['text']}...")

    print(f"{'='*50}\n")
