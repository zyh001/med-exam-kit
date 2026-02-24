from __future__ import annotations
from abc import ABC, abstractmethod
from pathlib import Path
from med_exam_toolkit.models import Question

MAX_OPTIONS = 10


class BaseExporter(ABC):

    @abstractmethod
    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        ...

    @staticmethod
    def _detect_max_options(questions: list[Question]) -> int:
        """扫描所有题目，确定最大选项数"""
        max_opt = 0
        for q in questions:
            for sq in q.sub_questions:
                max_opt = max(max_opt, len(sq.options))
        return min(max_opt, MAX_OPTIONS)

    @staticmethod
    def get_columns(max_opt: int) -> list[str]:
        """动态生成列名"""
        base = [
            "fingerprint", "pkg", "cls", "unit", "mode", "stem",
            "sub_index", "text",
        ]
        opt_cols = [f"option_{chr(65 + i)}" for i in range(max_opt)]
        tail = [
            # 有效值（官方优先，为空时 fallback 到 AI）
            "answer", "answer_source",
            "rate", "error_prone",
            "discuss", "discuss_source",
            "point",
            # AI 原始输出（无论官方字段是否为空都单独保留）
            "ai_answer", "ai_discuss", "ai_confidence", "ai_model",
        ]
        return base + opt_cols + tail

    @staticmethod
    def flatten(
        questions: list[Question],
        split_options: bool = True,
    ) -> tuple[list[dict], list[str]]:
        """
        展平 Question 列表为行记录。

        列说明：
          answer / discuss        → eff_answer / eff_discuss（官方优先，空时用 AI 兜底）
          answer_source           → "official" / "ai" / ""
          ai_answer / ai_discuss  → AI 原始输出，无论官方字段是否有值都单独输出
          ai_confidence           → AI 置信度
          ai_model                → 生成该 AI 结果的模型名
        """
        # 先确定最大选项数
        max_opt = 0
        for q in questions:
            for sq in q.sub_questions:
                max_opt = max(max_opt, len(sq.options))
        max_opt = min(max_opt, MAX_OPTIONS)

        if split_options:
            columns = BaseExporter.get_columns(max_opt)
        else:
            columns = [
                "fingerprint", "pkg", "cls", "unit", "mode", "stem",
                "sub_index", "text", "options",
                "answer", "answer_source",
                "rate", "error_prone",
                "discuss", "discuss_source",
                "point",
                "ai_answer", "ai_discuss", "ai_confidence", "ai_model",
            ]

        rows = []
        for q in questions:
            base = {
                "fingerprint": q.fingerprint,
                "pkg": q.pkg,
                "cls": q.cls,
                "unit": q.unit,
                "mode": q.mode,
                "stem": q.stem,
            }
            for i, sq in enumerate(q.sub_questions, 1):
                row = {
                    **base,
                    "sub_index":      i,
                    "text":           sq.text,
                    # 有效值（官方优先 fallback AI）
                    "answer":         sq.eff_answer,
                    "answer_source":  sq.answer_source,
                    "rate":           sq.rate,
                    "error_prone":    sq.error_prone,
                    "discuss":        sq.eff_discuss,
                    "discuss_source": sq.discuss_source,
                    "point":          sq.point,
                    # AI 原始输出（单独列，供对比/审核）
                    "ai_answer":      (sq.ai_answer or "").strip(),
                    "ai_discuss":     (sq.ai_discuss or "").strip(),
                    "ai_confidence":  sq.ai_confidence if sq.ai_confidence else "",
                    "ai_model":       sq.ai_model or "",
                }

                if split_options:
                    for j, opt in enumerate(sq.options[:max_opt]):
                        row[f"option_{chr(65 + j)}"] = opt
                    # 不足的列留空
                    for j in range(len(sq.options), max_opt):
                        row[f"option_{chr(65 + j)}"] = ""
                else:
                    row["options"] = " | ".join(sq.options)

                rows.append(row)

        return rows, columns