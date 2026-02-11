"""题目过滤器"""
from __future__ import annotations
import re
from dataclasses import dataclass, field
from med_exam_toolkit.models import Question


@dataclass
class FilterCriteria:
    """过滤条件，所有条件取交集"""
    modes: list[str] = field(default_factory=list)       # 题型白名单
    units: list[str] = field(default_factory=list)       # 章节关键词
    pkgs: list[str] = field(default_factory=list)        # 来源白名单
    cls_list: list[str] = field(default_factory=list)    # 题库白名单
    keyword: str = ""                                     # 题干关键词搜索
    min_rate: int = 0                                     # 最低正确率
    max_rate: int = 100                                   # 最高正确率


def apply_filters(questions: list[Question], criteria: FilterCriteria) -> list[Question]:
    """根据条件过滤题目"""
    result = []

    for q in questions:
        # 题型过滤
        if criteria.modes:
            if not any(m in q.mode for m in criteria.modes):
                continue

        # 来源过滤
        if criteria.pkgs:
            if not any(p in q.pkg for p in criteria.pkgs):
                continue

        # 题库过滤
        if criteria.cls_list:
            if not any(c in q.cls for c in criteria.cls_list):
                continue

        # 章节关键词过滤
        if criteria.units:
            if not any(u in q.unit for u in criteria.units):
                continue

        # 题干关键词搜索
        if criteria.keyword:
            kw = criteria.keyword.lower()
            texts = [sq.text.lower() for sq in q.sub_questions]
            stem = (q.stem or "").lower()
            if not any(kw in t for t in texts) and kw not in stem:
                continue

        # 正确率范围过滤
        if criteria.min_rate > 0 or criteria.max_rate < 100:
            rates = []
            for sq in q.sub_questions:
                if sq.rate:
                    try:
                        rates.append(int(sq.rate.replace("%", "").strip()))
                    except ValueError:
                        pass
            if rates:
                avg_rate = sum(rates) / len(rates)
                if avg_rate < criteria.min_rate or avg_rate > criteria.max_rate:
                    continue

        result.append(q)

    print(f"[INFO] 过滤完成: {len(questions)} -> {len(result)}")
    return result
