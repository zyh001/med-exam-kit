# src/med_exam_toolkit/exporters/csv_exporter.py （改进版）
from __future__ import annotations
import csv
from pathlib import Path
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter


@register("csv")
class CsvExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        split_options = kwargs.get("split_options", True)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".csv")

        rows, columns = self.flatten(questions, split_options=split_options)

        with open(fp, "w", newline="", encoding="utf-8-sig") as f:
            writer = csv.DictWriter(f, fieldnames=columns, extrasaction="ignore")
            writer.writeheader()
            writer.writerows(rows)

        print(f"[INFO] CSV 导出完成: {fp} ({len(rows)} 行, {len(columns)} 列)")
