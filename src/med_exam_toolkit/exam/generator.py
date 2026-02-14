"""自动组卷引擎"""
from __future__ import annotations
import random
from collections import Counter, defaultdict
from med_exam_toolkit.models import Question
from med_exam_toolkit.exam.config import ExamConfig


class ExamGenerationError(Exception):
    pass


class ExamGenerator:

    def __init__(self, questions: list[Question], config: ExamConfig):
        self.pool = questions
        self.config = config
        self._rng = random.Random(config.seed)

    def generate(self) -> list[Question]:
        filtered = self._filter_pool()
        if not filtered:
            raise ExamGenerationError("筛选后题库为空，请检查 units / modes 条件")

        if self.config.per_mode:
            selected = self._sample_per_mode(filtered)
        else:
            selected = self._sample_total(filtered)

        self._rng.shuffle(selected)

        order = self._mode_order()
        selected.sort(key=lambda q: order.get(q.mode, 99))
        return selected

    def summary(self, selected: list[Question]) -> str:
        by_mode = Counter(q.mode for q in selected)
        by_unit = Counter(q.unit for q in selected)
        lines = [
            f"试卷: {self.config.title}",
            f"总题数: {len(selected)}",
            f"题型分布: {dict(by_mode.most_common())}",
            f"章节分布: {dict(by_unit.most_common(10))}",
        ]
        return "\n".join(lines)

    # ── 内部 ──

    def _filter_pool(self) -> list[Question]:
        pool = self.pool
        if self.config.units:
            units_lower = {u.lower() for u in self.config.units}
            pool = [q for q in pool if q.unit.lower() in units_lower]
        if self.config.modes:
            modes_lower = {m.lower() for m in self.config.modes}
            pool = [q for q in pool if q.mode.lower() in modes_lower]
        return pool

    def _sample_total(self, pool: list[Question]) -> list[Question]:
        n = min(self.config.count, len(pool))
        if n < self.config.count:
            print(f"[WARN] 可用题目 {len(pool)} 道，不足 {self.config.count}，将全部使用")
        return self._rng.sample(pool, n)

    def _sample_per_mode(self, pool: list[Question]) -> list[Question]:
        by_mode: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            by_mode[q.mode].append(q)

        selected = []
        for mode, need in self.config.per_mode.items():
            available = by_mode.get(mode, [])
            n = min(need, len(available))
            if n < need:
                print(f"[WARN] {mode} 可用 {len(available)} 道，不足 {need}，将全部使用")
            selected.extend(self._rng.sample(available, n))
        return selected

    @staticmethod
    def _mode_order() -> dict[str, int]:
        return {
            "A1型题": 0, "A2型题": 1,
            "A3/A4型题": 2, "A3型题": 2, "A4型题": 2,
            "B1型题": 3, "B型题": 3,
            "案例分析": 4,
        }
