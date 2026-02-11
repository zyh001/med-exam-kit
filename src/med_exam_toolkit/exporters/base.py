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
        # 动态选项列: option_A, option_B, ...
        opt_cols = [f"option_{chr(65 + i)}" for i in range(max_opt)]
        tail = ["answer", "rate", "error_prone", "discuss", "point"]
        return base + opt_cols + tail

    @staticmethod
    def flatten(questions: list[Question], split_options: bool = True) -> tuple[list[dict], list[str]]:
        """
        将 Question 列表展平为行记录。

        返回 (rows, columns)
        split_options=True:  每个选项独立一列 (option_A, option_B, ...)
        split_options=False: 所有选项合并为一列 (options)
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
                "answer", "rate", "error_prone", "discuss", "point",
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
                    "sub_index": i,
                    "text": sq.text,
                    "answer": sq.answer,
                    "rate": sq.rate,
                    "error_prone": sq.error_prone,
                    "discuss": sq.discuss,
                    "point": sq.point,
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
