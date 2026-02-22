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
        self._by_sub = config.count_mode == "sub"

    # ── 计数工具 ──

    @staticmethod
    def _sub_count(q: Question) -> int:
        return len(q.sub_questions)

    def _cost(self, q: Question) -> int:
        """一道题的"花费"：小题模式返回子题数，大题模式返回 1"""
        return self._sub_count(q) if self._by_sub else 1

    def _total_cost(self, qs: list[Question]) -> int:
        return sum(self._cost(q) for q in qs)

    @staticmethod
    def _total_subs(qs: list[Question]) -> int:
        return sum(len(q.sub_questions) for q in qs)

    # ── 核心抽取 ──

    def _greedy_fill(self, pool: list[Question], target: int, used_ids: set[int]) -> list[Question]:
        available = [q for q in pool if id(q) not in used_ids]
        self._rng.shuffle(available)
        picked = []
        total = 0

        # 第一轮：贪心塞能放下的
        remaining = []
        for q in available:
            c = self._cost(q)
            if total + c <= target:
                picked.append(q)
                used_ids.add(id(q))
                total += c
                if total == target:
                    return picked
            else:
                remaining.append(q)

        # 第二轮：找 cost <= gap 且最大的
        while total < target and remaining:
            gap = target - total
            best = None
            best_c = 0
            best_idx = -1
            for i, q in enumerate(remaining):
                c = self._cost(q)
                if c <= gap and c > best_c:
                    best = q
                    best_c = c
                    best_idx = i
            if best is not None:
                picked.append(best)
                used_ids.add(id(best))
                total += best_c
                remaining.pop(best_idx)
                if total == target:
                    return picked
            else:
                break

        # 第三轮（仅小题模式）：截断凑满
        if self._by_sub and total < target and remaining:
            gap = target - total

            # 优先找子题数刚好 == gap 的
            exact = None
            exact_idx = -1
            for i, q in enumerate(remaining):
                if self._sub_count(q) == gap:
                    exact = q
                    exact_idx = i
                    break
            if exact is not None:
                picked.append(exact)
                used_ids.add(id(exact))
                remaining.pop(exact_idx)
                return picked

            # 找不到精确匹配，选子题数最少且 > gap 的截断
            candidates = [(i, q) for i, q in enumerate(remaining) if self._sub_count(q) > gap]
            if candidates:
                candidates.sort(key=lambda x: self._sub_count(x[1]))
                idx, q = candidates[0]
                sc = self._sub_count(q)
                q.sub_questions = q.sub_questions[:gap]
                picked.append(q)
                used_ids.add(id(q))
                print(f"[INFO] 截断大题（原 {sc} 子题 → 保留 {gap} 子题）")

        return picked

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
        selected.sort(key=lambda q: (order.get(q.mode, 99), q.unit))
        return selected

    def summary(self, selected: list[Question]) -> str:
        total_subs = self._total_subs(selected)
        by_mode_subs = Counter()
        by_mode_q = Counter()
        for q in selected:
            by_mode_subs[q.mode] += self._sub_count(q)
            by_mode_q[q.mode] += 1
        by_unit = Counter()
        for q in selected:
            by_unit[q.unit] += self._sub_count(q)

        mode_str = ", ".join(
            f"{m}: {by_mode_subs[m]}小题({by_mode_q[m]}大题)"
            for m, _ in by_mode_subs.most_common()
        )

        lines = [
            f"试卷: {self.config.title}",
            f"计数模式: {'按小题' if self._by_sub else '按大题'}",
            f"总题数: {total_subs} 小题（{len(selected)} 大题）",
            f"题型分布: {mode_str}",
            f"章节分布: {dict(by_unit.most_common(10))}",
        ]

        # 分数
        score_info = self._calc_score(selected)
        if score_info:
            lines.append(score_info)

        if self.config.difficulty_dist:
            by_diff = Counter()
            for q in selected:
                by_diff[self._classify_difficulty(q)] += self._sub_count(q)
            diff_str = ", ".join(
                f"{DIFFICULTY_LABELS.get(k, k)}: {v}道"
                for k, v in sorted(
                    by_diff.items(),
                    key=lambda x: list(DIFFICULTY_LABELS).index(x[0])
                    if x[0] in DIFFICULTY_LABELS else 99,
                )
            )
            lines.append(f"难度分布: {diff_str}")
        return "\n".join(lines)

    def _calc_score(self, selected: list[Question]) -> str | None:
        total_subs = self._total_subs(selected)
        if not total_subs:
            return None

        if self.config.score_per_sub:
            per = self.config.score_per_sub
            total = per * total_subs
            return f"分值: 每小题 {per} 分，总分 {total} 分"

        if self.config.total_score:
            per = round(self.config.total_score / total_subs, 2)
            total = self.config.total_score
            return f"分值: 总分 {total} 分，每小题 {per} 分"

        return None

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

    # ── 筛选 ──

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

    # ── 抽样策略 ──

    def _sample_total(self, pool: list[Question]) -> list[Question]:
        target = self.config.count
        pool_cost = self._total_cost(pool)
        if pool_cost < target:
            unit = "小题" if self._by_sub else "大题"
            print(f"[WARN] 可用{unit} {pool_cost} 道，不足 {target}，将全部使用")
            return list(pool)
        return self._greedy_fill(pool, target, set())

    def _sample_per_mode(self, pool: list[Question]) -> list[Question]:
        by_mode: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            by_mode[q.mode].append(q)

        selected = []
        used_ids: set[int] = set()
        unit = "小题" if self._by_sub else "大题"
        for mode, need in self.config.per_mode.items():
            available = by_mode.get(mode, [])
            avail_cost = self._total_cost([q for q in available if id(q) not in used_ids])
            if avail_cost < need:
                print(f"[WARN] {mode} 可用{unit} {avail_cost} 道，不足 {need}，将全部使用")
            picked = self._greedy_fill(available, need, used_ids)
            selected.extend(picked)
        return selected

    def _sample_from_pool_by_difficulty(
        self, pool: list[Question], total: int, used_ids: set[int] | None = None,
    ) -> list[Question]:
        if used_ids is None:
            used_ids = set()

        dist = self.config.difficulty_dist
        by_diff: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            if id(q) not in used_ids:
                by_diff[self._classify_difficulty(q)].append(q)

        targets = self._distribute_by_ratio(total, dist)

        selected = []
        shortfall = 0
        for level, need in targets.items():
            available = by_diff.get(level, [])
            picked = self._greedy_fill(available, need, used_ids)
            got = self._total_cost(picked)
            if got < need:
                label = DIFFICULTY_LABELS.get(level, level)
                print(f"[WARN] {label}({level}) 可用 {got} 道，不足 {need}")
                shortfall += need - got
            selected.extend(picked)

        # 不够的从剩余题目补齐
        if shortfall > 0:
            remaining = [q for q in pool if id(q) not in used_ids]
            filled = self._greedy_fill(remaining, shortfall, used_ids)
            fill_count = self._total_cost(filled)
            if fill_count > 0:
                selected.extend(filled)
                print(f"[INFO] 从其他难度补充 {fill_count} 道")

        return selected

    def _sample_by_difficulty(self, pool: list[Question]) -> list[Question]:
        target = self.config.count
        pool_cost = self._total_cost(pool)
        if pool_cost < target:
            unit = "小题" if self._by_sub else "大题"
            print(f"[WARN] 可用{unit} {pool_cost} 道，不足 {target}，将全部使用")
            return list(pool)
        return self._sample_from_pool_by_difficulty(pool, target)

    # ── 先题型后难度 ──
    def _sample_per_mode_with_difficulty(self, pool: list[Question]) -> list[Question]:
        """先按题型分，再在每个题型内按难度比例抽取"""
        by_mode: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            by_mode[q.mode].append(q)

        selected = []
        used_ids: set[int] = set()
        unit = "小题" if self._by_sub else "大题"
        for mode, need in self.config.per_mode.items():
            mode_pool = by_mode.get(mode, [])
            if not mode_pool:
                print(f"[WARN] {mode} 无可用题目")
                continue
            avail_cost = self._total_cost([q for q in mode_pool if id(q) not in used_ids])
            if avail_cost < need:
                print(f"[WARN] {mode} 可用{unit} {avail_cost} 道，不足 {need}")
            batch = self._sample_from_pool_by_difficulty(mode_pool, need, used_ids)
            selected.extend(batch)
        return selected

    # ── 先难度后题型 (默认) ──
    def _sample_global_difficulty_then_mode(self, pool: list[Question]) -> list[Question]:
        dist = self.config.difficulty_dist
        per_mode = self.config.per_mode
        total_need = sum(per_mode.values())

        # 1) 全局按难度分桶
        by_diff: dict[str, list[Question]] = defaultdict(list)
        for q in pool:
            if q.mode in per_mode:
                by_diff[self._classify_difficulty(q)].append(q)

        # 2) 每个难度档需要多少题
        diff_targets = self._distribute_by_ratio(total_need, dist)

        # 3) 区分单子题题型和多子题题型
        multi_sub_modes = {m for m in per_mode if any(
            self._sub_count(q) > 1 for q in pool if q.mode == m
        )}
        single_sub_modes = {m: n for m, n in per_mode.items() if m not in multi_sub_modes}
        multi_sub_mode_targets = {m: n for m, n in per_mode.items() if m in multi_sub_modes}

        selected = []
        used_ids: set[int] = set()

        # 4) 先在每个难度档内分配单子题题型（这些不会截断）
        for level, diff_need in diff_targets.items():
            diff_pool = by_diff.get(level, [])
            by_mode_in_diff: dict[str, list[Question]] = defaultdict(list)
            for q in diff_pool:
                if id(q) not in used_ids and q.mode in single_sub_modes:
                    by_mode_in_diff[q.mode].append(q)

            if single_sub_modes:
                single_need = sum(
                    round(diff_need * single_sub_modes[m] / total_need)
                    for m in single_sub_modes
                )
                mode_targets = self._distribute_by_ratio(
                    min(single_need, diff_need), single_sub_modes
                )
                for mode, need in mode_targets.items():
                    available = by_mode_in_diff.get(mode, [])
                    picked = self._greedy_fill(available, need, used_ids)
                    selected.extend(picked)

        # 5) 多子题题型：不按难度档细分，直接从全池按难度优先级整题抽取
        for mode, need in multi_sub_mode_targets.items():
            mode_pool = [q for q in pool if q.mode == mode and id(q) not in used_ids]

            # 按难度优先级排序：让目标难度的题排前面
            diff_order = list(dist.keys())
            mode_pool_by_diff: list[Question] = []
            for level in diff_order:
                batch = [q for q in mode_pool if self._classify_difficulty(q) == level]
                self._rng.shuffle(batch)
                mode_pool_by_diff.extend(batch)

            picked = self._greedy_fill(mode_pool_by_diff, need, used_ids)
            selected.extend(picked)

        # 6) 按题型补齐
        mode_cost = Counter()
        for q in selected:
            mode_cost[q.mode] += self._cost(q)

        unit = "小题" if self._by_sub else "大题"
        for mode, need in per_mode.items():
            got = mode_cost.get(mode, 0)
            if got < need:
                shortfall = need - got
                remaining = [q for q in pool if q.mode == mode and id(q) not in used_ids]
                filled = self._greedy_fill(remaining, shortfall, used_ids)
                fill_cost = self._total_cost(filled)
                selected.extend(filled)
                mode_cost[mode] = got + fill_cost
                if got + fill_cost < need:
                    print(f"[WARN] {mode} 最终只能凑到 {got + fill_cost} 道{unit}，不足 {need}")

        # 最终总量校验：如果超出 total_need，从多子题题型末尾截断
        total_cost = sum(self._cost(q) for q in selected)
        if total_cost > total_need:
            overflow = total_cost - total_need
            # 从后往前找多子题的大题，截断子题
            for q in reversed(selected):
                if overflow <= 0:
                    break
                sc = self._sub_count(q)
                if sc > 1:
                    can_trim = min(sc - 1, overflow)
                    q.sub_questions = q.sub_questions[:sc - can_trim]
                    overflow -= can_trim
            # 如果截断后还多，移除整题
            while overflow > 0:
                for i in range(len(selected) - 1, -1, -1):
                    c = self._cost(selected[i])
                    if c <= overflow:
                        overflow -= c
                        used_ids.discard(id(selected[i]))
                        selected.pop(i)
                        break
                else:
                    break

        return selected

    @staticmethod
    def _mode_order() -> dict[str, int]:
        return {
            "A1型题": 0, "A2型题": 1,
            "A3/A4型题": 2, "A3型题": 2, "A4型题": 2,
            "B1型题": 3, "B型题": 3,
            "案例分析": 4,
        }
