"""自动组卷引擎"""
from __future__ import annotations
import random
from collections import Counter, defaultdict
from med_exam_toolkit.models import Question
from med_exam_toolkit.exam.config import ExamConfig

DIFFICULTY_LABELS = {
    "easy": "简单",
    "medium": "中等",
    "hard": "较难",
    "extreme": "困难",
}


class ExamGenerationError(Exception):
    pass


class ExamGenerator:

    def __init__(self, questions: list[Question], config: ExamConfig):
        self.pool = questions
        self.config = config
        self._rng = random.Random(config.seed)

    # ── 公开接口 ──

    def generate(self) -> list[Question]:
        filtered = self._filter_pool()
        if not filtered:
            raise ExamGenerationError("筛选后题库为空，请检查 units / modes 条件")

        has_per_mode = bool(self.config.per_mode)
        has_difficulty = bool(self.config.difficulty_dist)

        if has_per_mode and has_difficulty:
            if self.config.difficulty_mode == "per_mode":
                selected = self._sample_per_mode_with_difficulty(filtered)
            else:
                selected = self._sample_global_difficulty_then_mode(filtered)
        elif has_per_mode:
            selected = self._sample_per_mode(filtered)
        elif has_difficulty:
            selected = self._sample_by_difficulty(filtered)
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
        if self.config.difficulty_dist:
            by_diff = Counter(self._classify_difficulty(q) for q in selected)
            diff_str = ", ".join(
                f"{DIFFICULTY_LABELS.get(k, k)}: {v}道"
                for k, v in sorted(by_diff.items(), key=lambda x: list(DIFFICULTY_LABELS).index(x[0]) if x[0] in DIFFICULTY_LABELS else 99)
            )
            lines.append(f"难度分布: {diff_str}")
        return "\n".join(lines)

    # ── 难度分级 ──

    @staticmethod
    def _parse_rate(raw: str) -> float | None:
        """解析正确率字符串 → 0~100 浮点数"""
        if not raw or not raw.strip():
            return None
        s = raw.strip().rstrip("%")
        try:
            v = float(s)
            return v if 0 <= v <= 100 else None
        except ValueError:
            return None

    def _classify_difficulty(self, q: Question) -> str:
        """按子题平均正确率分四档，无数据默认 medium"""
        rates = []
        for sq in q.sub_questions:
            r = self._parse_rate(sq.rate)
            if r is not None:
                rates.append(r)
        if not rates:
            return "medium"
        avg = sum(rates) / len(rates)
        if avg >= 80:
            return "easy"
        if avg >= 60:
            return "medium"
        if avg >= 40:
            return "hard"
        return "extreme"

    # ── 按比例分配 ──

    def _distribute_by_ratio(self, total: int, ratios: dict[str, int]) -> dict[str, int]:
        """将 total 按比例分配为整数，保证总和 == total"""
        ratio_sum = sum(ratios.values())
        if ratio_sum == 0:
            return {k: 0 for k in ratios}
        result = {}
        allocated = 0
        items = list(ratios.items())
        for i, (key, pct) in enumerate(items):
            if i == len(items) - 1:
                result[key] = total - allocated
            else:
                n = round(total * pct / ratio_sum)
                result[key] = n
                allocated += n
        return result

    # ── 抽样策略 ──

    def _filter_pool(self) -> list[Question]:
        pool = self.pool
        if self.config.cls_list:
            cls_lower = {c.lower() for c in self.config.cls_list}
            pool = [q for q in pool if q.cls.lower() in cls_lower]
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

    def _sample_from_pool_by_difficulty(self, pool: list[Question], total: int) -> list[Question]:
        """核心: 从 pool 中按难度比例抽取 total 道题"""
        dist = self.config.difficulty_dist
        by_diff: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            by_diff[self._classify_difficulty(q)].append(q)

        targets = self._distribute_by_ratio(total, dist)

        selected = []
        shortfall = 0
        for level, need in targets.items():
            available = by_diff.get(level, [])
            n = min(need, len(available))
            if n < need:
                label = DIFFICULTY_LABELS.get(level, level)
                print(f"[WARN] {label}({level}) 可用 {len(available)} 道，不足 {need}")
                shortfall += need - n
            if n > 0:
                selected.extend(self._rng.sample(available, n))

        # 不够的从剩余题目补齐
        if shortfall > 0:
            used = {id(q) for q in selected}
            remaining = [q for q in pool if id(q) not in used]
            fill = min(shortfall, len(remaining))
            if fill > 0:
                selected.extend(self._rng.sample(remaining, fill))
                print(f"[INFO] 从其他难度补充 {fill} 道")

        return selected

    def _sample_by_difficulty(self, pool: list[Question]) -> list[Question]:
        """仅按难度比例，不分题型"""
        total = min(self.config.count, len(pool))
        if total < self.config.count:
            print(f"[WARN] 可用题目 {len(pool)} 道，不足 {self.config.count}，将全部使用")
        return self._sample_from_pool_by_difficulty(pool, total)

    # ── 先题型后难度 ──
    def _sample_per_mode_with_difficulty(self, pool: list[Question]) -> list[Question]:
        """先按题型分，再在每个题型内按难度比例抽取"""
        by_mode: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            by_mode[q.mode].append(q)

        selected = []
        for mode, need in self.config.per_mode.items():
            mode_pool = by_mode.get(mode, [])
            if not mode_pool:
                print(f"[WARN] {mode} 无可用题目")
                continue
            n = min(need, len(mode_pool))
            if n < need:
                print(f"[WARN] {mode} 可用 {len(mode_pool)} 道，不足 {need}")
            batch = self._sample_from_pool_by_difficulty(mode_pool, n)
            selected.extend(batch)
        return selected

    # ── 先难度后题型 (默认) ──
    def _sample_global_difficulty_then_mode(self, pool: list[Question]) -> list[Question]:
        dist = self.config.difficulty_dist
        per_mode = self.config.per_mode
        total_need = sum(per_mode.values())

        # 1) 全局按难度分桶（只保留目标题型）
        by_diff: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            if q.mode in per_mode:
                by_diff[self._classify_difficulty(q)].append(q)

        # 2) 每个难度档需要多少题
        diff_targets = self._distribute_by_ratio(total_need, dist)

        # 3) 在每个难度档内按题型比例分配
        selected = []
        used_ids: set[int] = set()

        for level, diff_need in diff_targets.items():
            diff_pool = by_diff.get(level, [])
            by_mode_in_diff: dict[str, list[Question]] = defaultdict(list)
            for q in diff_pool:
                by_mode_in_diff[q.mode].append(q)

            mode_targets = self._distribute_by_ratio(diff_need, per_mode)

            for mode, need in mode_targets.items():
                available = [q for q in by_mode_in_diff.get(mode, []) if id(q) not in used_ids]
                n = min(need, len(available))
                if n > 0:
                    picked = self._rng.sample(available, n)
                    selected.extend(picked)
                    used_ids.update(id(q) for q in picked)

        # 4) 按题型补齐缺口
        mode_count = Counter(q.mode for q in selected)
        for mode, need in per_mode.items():
            got = mode_count.get(mode, 0)
            if got < need:
                shortfall = need - got
                remaining = [q for q in pool if q.mode == mode and id(q) not in used_ids]
                fill = min(shortfall, len(remaining))
                if fill > 0:
                    picked = self._rng.sample(remaining, fill)
                    selected.extend(picked)
                    used_ids.update(id(q) for q in picked)
                    mode_count[mode] = got + fill
                if fill < shortfall:
                    print(f"[WARN] {mode} 最终只能凑到 {got + fill} 道，不足 {need}")

        return selected

    @staticmethod
    def _mode_order() -> dict[str, int]:
        return {
            "A1型题": 0, "A2型题": 1,
            "A3/A4型题": 2, "A3型题": 2, "A4型题": 2,
            "B1型题": 3, "B型题": 3,
            "案例分析": 4,
        }
