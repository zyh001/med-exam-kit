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
            d.pop("raw", None)
            # 用 eff_answer/eff_discuss 覆盖序列化值，确保 AI 兜底生效
            for sq_raw, sq_obj in zip(d["sub_questions"], q.sub_questions):
                sq_raw["answer"]  = sq_obj.eff_answer
                sq_raw["discuss"] = sq_obj.eff_discuss
                # 保留来源标注，方便下游判断
                sq_raw["answer_source"]  = sq_obj.answer_source
                sq_raw["discuss_source"] = sq_obj.discuss_source
            data.append(d)

        fp.write_text(
            json.dumps(data, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        print(f"[INFO] JSON 导出完成: {fp} ({len(data)} 题)")
