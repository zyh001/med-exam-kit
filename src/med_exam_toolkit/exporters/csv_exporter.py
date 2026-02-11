from __future__ import annotations
import csv
from pathlib import Path
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter

COLUMNS = [
    "fingerprint", "pkg", "cls", "unit", "mode", "stem",
    "sub_index", "text", "options", "answer", "rate",
    "error_prone", "discuss", "point",
]


@register("csv")
class CsvExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".csv")
        rows = self.flatten(questions)

        with open(fp, "w", newline="", encoding="utf-8-sig") as f:
            writer = csv.DictWriter(f, fieldnames=COLUMNS)
            writer.writeheader()
            writer.writerows(rows)

        print(f"[INFO] CSV 导出完成: {fp} ({len(rows)} 行)")
