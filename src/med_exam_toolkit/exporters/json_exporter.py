from __future__ import annotations
import json
from pathlib import Path
from dataclasses import asdict
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter


@register("json")
class JsonExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".json")

        data = []
        for q in questions:
            d = asdict(q)
            d.pop("raw", None)  # 去掉原始 JSON 减小体积
            data.append(d)

        fp.write_text(
            json.dumps(data, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        print(f"[INFO] JSON 导出完成: {fp} ({len(data)} 题)")
