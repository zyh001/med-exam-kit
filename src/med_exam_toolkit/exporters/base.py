# src/med_exam_toolkit/exporters/base.py
from __future__ import annotations
from abc import ABC, abstractmethod
from pathlib import Path
from med_exam_toolkit.models import Question


class BaseExporter(ABC):

    @abstractmethod
    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        ...

    @staticmethod
    def flatten(questions: list[Question]) -> list[dict]:
        """
        将 Question 列表展平为行记录，方便表格类导出。
        每个 sub_question 展开为一行。
        """
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
                    "options": " | ".join(sq.options),
                    "answer": sq.answer,
                    "rate": sq.rate,
                    "error_prone": sq.error_prone,
                    "discuss": sq.discuss,
                    "point": sq.point,
                }
                rows.append(row)
        return rows
