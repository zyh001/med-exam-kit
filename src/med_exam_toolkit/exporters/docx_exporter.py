from __future__ import annotations
from pathlib import Path
from docx import Document
from docx.shared import Pt, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter


@register("docx")
class DocxExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".docx")

        doc = Document()
        style = doc.styles["Normal"]
        style.font.name = "微软雅黑"
        style.font.size = Pt(10.5)

        doc.add_heading("医学考试题库", level=0).alignment = WD_ALIGN_PARAGRAPH.CENTER

        for idx, q in enumerate(questions, 1):
            # 题目标题
            heading = f"第{idx}题 [{q.mode}] {q.unit}"
            doc.add_heading(heading, level=2)

            if q.stem:
                p = doc.add_paragraph()
                run = p.add_run(f"【题干】{q.stem}")
                run.font.size = Pt(10.5)

            if q.shared_options:
                p = doc.add_paragraph()
                run = p.add_run("【共享选项】")
                run.bold = True
                for opt in q.shared_options:
                    doc.add_paragraph(opt, style="List Bullet")

            for si, sq in enumerate(q.sub_questions, 1):
                # 子题
                prefix = f"({si}) " if len(q.sub_questions) > 1 else ""
                p = doc.add_paragraph()
                run = p.add_run(f"{prefix}{sq.text}")
                run.bold = True

                if sq.options and sq.options != q.shared_options:
                    for opt in sq.options:
                        doc.add_paragraph(opt, style="List Bullet")

                # 答案
                p = doc.add_paragraph()
                run = p.add_run(f"答案: {sq.answer}")
                run.font.color.rgb = RGBColor(0, 128, 0)
                if sq.rate:
                    p.add_run(f"  正确率: {sq.rate}")

                # 解析
                discuss = sq.discuss or q.discuss
                if discuss:
                    p = doc.add_paragraph()
                    run = p.add_run(f"解析: {discuss}")
                    run.font.size = Pt(9)
                    run.font.color.rgb = RGBColor(100, 100, 100)

            doc.add_paragraph("—" * 40)

        doc.save(fp)
        print(f"[INFO] DOCX 导出完成: {fp} ({len(questions)} 题)")
