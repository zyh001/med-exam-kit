"""题目统计分析"""
from __future__ import annotations
from collections import Counter, defaultdict
from med_exam_toolkit.models import Question


def summarize(questions: list[Question]) -> dict:
    """生成统计摘要"""
    by_mode = Counter()
    by_unit = Counter()
    by_pkg = Counter()
    by_cls = Counter()
    low_rate_questions = []  # 正确率低于 50% 的题

    for q in questions:
        by_mode[q.mode] += 1
        by_unit[q.unit] += 1
        by_pkg[q.pkg] += 1
        by_cls[q.cls] += 1

        for sq in q.sub_questions:
            if sq.rate:
                try:
                    rate_val = int(sq.rate.replace("%", "").strip())
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

    return {
        "total": len(questions),
        "by_mode": dict(by_mode.most_common()),
        "by_unit": dict(by_unit.most_common(20)),
        "by_pkg": dict(by_pkg.most_common()),
        "by_cls": dict(by_cls.most_common()),
        "low_rate_count": len(low_rate_questions),
        "low_rate_top10": sorted(low_rate_questions, key=lambda x: x["rate"])[:10],
    }


def print_summary(questions: list[Question]) -> None:
    """打印统计摘要到终端"""
    s = summarize(questions)
    print(f"\n{'='*50}")
    print(f"📊 题目统计")
    print(f"{'='*50}")
    print(f"总题数: {s['total']}")

    print(f"\n按题型:")
    for mode, count in s["by_mode"].items():
        print(f"  {mode}: {count}")

    print(f"\n按来源:")
    for pkg, count in s["by_pkg"].items():
        print(f"  {pkg}: {count}")

    print(f"\n按题库:")
    for cls, count in s["by_cls"].items():
        print(f"  {cls}: {count}")

    print(f"\n按章节 (Top 10):")
    for unit, count in list(s["by_unit"].items())[:10]:
        print(f"  {unit}: {count}")

    if s["low_rate_count"]:
        print(f"\n⚠️  正确率 < 50% 的题目: {s['low_rate_count']} 道")
        for item in s["low_rate_top10"]:
            print(f"  [{item['rate']}] {item['text']}...")

    print(f"{'='*50}\n")
