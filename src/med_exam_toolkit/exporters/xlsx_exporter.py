# src/med_exam_toolkit/exporters/xlsx_exporter.py
from __future__ import annotations
from pathlib import Path
from openpyxl import Workbook
from openpyxl.styles import Font, Alignment, PatternFill
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter

COLUMNS = [
    "fingerprint", "pkg", "cls", "unit", "mode", "stem",
    "sub_index", "text", "options", "answer", "rate",
    "error_prone", "discuss", "point",
]

HEADER_LABELS = {
    "fingerprint": "指纹", "pkg": "来源", "cls": "题库", "unit": "章节",
    "mode": "题型", "stem": "共享题干", "sub_index": "子题序号",
    "text": "题目", "options": "选项", "answer": "答案",
    "rate": "正确率", "error_prone": "易错项", "discuss": "解析", "point": "考点",
}


@register("xlsx")
class XlsxExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".xlsx")
        rows = self.flatten(questions)

        wb = Workbook()
        ws = wb.active
        ws.title = "题目"

        # 表头
        header_font = Font(bold=True, color="FFFFFF")
        header_fill = PatternFill(start_color="4472C4", end_color="4472C4", fill_type="solid")
        for col_idx, col_key in enumerate(COLUMNS, 1):
            cell = ws.cell(row=1, column=col_idx, value=HEADER_LABELS.get(col_key, col_key))
            cell.font = header_font
            cell.fill = header_fill
            cell.alignment = Alignment(horizontal="center")

        # 数据
        for row_idx, row in enumerate(rows, 2):
            for col_idx, col_key in enumerate(COLUMNS, 1):
                cell = ws.cell(row=row_idx, column=col_idx, value=row.get(col_key, ""))
                cell.alignment = Alignment(wrap_text=True, vertical="top")

        # 列宽
        col_widths = {
            "text": 50, "options": 60, "discuss": 50, "point": 40,
            "stem": 50, "unit": 20, "cls": 20,
        }
        for col_idx, col_key in enumerate(COLUMNS, 1):
            ws.column_dimensions[chr(64 + col_idx) if col_idx <= 26 else "A"].width = col_widths.get(col_key, 14)

        wb.save(fp)
        print(f"[INFO] XLSX 导出完成: {fp} ({len(rows)} 行)")
