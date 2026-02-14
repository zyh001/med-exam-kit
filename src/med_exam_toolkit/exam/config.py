"""组卷配置"""
from __future__ import annotations
from dataclasses import dataclass, field


@dataclass
class ExamConfig:
    title: str = "模拟考试"
    subtitle: str = ""
    time_limit: int = 120                    # 分钟
    total_score: int = 100

    # 抽题规则
    units: list[str] = field(default_factory=list)
    modes: list[str] = field(default_factory=list)
    count: int = 50
    per_mode: dict[str, int] = field(default_factory=dict)
    # 例: {"A1型题": 20, "A2型题": 15, "A3/A4型题": 10}

    seed: int | None = None
    show_answers: bool = False
    answer_sheet: bool = True
    show_discuss: bool = False
    score_per_question: float = 2.0
