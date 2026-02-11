from __future__ import annotations
from pathlib import Path
from openpyxl import Workbook
from openpyxl.styles import Font, Alignment, PatternFill
from openpyxl.utils import get_column_letter
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter

# 中文表头映射
HEADER_LABELS = {
    "fingerprint": "指纹", "pkg": "来源", "cls": "题库", "unit": "章节",
    "mode": "题型", "stem": "共享题干", "sub_index": "子题序号",
    "text": "题目", "options": "选项", "answer": "答案",
    "rate": "正确率", "error_prone": "易错项", "discuss": "解析", "point": "考点",
}

# 选项列中文映射
for i in range(10):
    HEADER_LABELS[f"option_{chr(65 + i)}"] = f"选项{chr(65 + i)}"

# 列宽配置
COL_WIDTHS = {
    "text": 50, "discuss": 50, "point": 40, "stem": 50,
    "unit": 20, "cls": 20, "options": 60,
}
# 选项列默认宽度
for i in range(10):
    COL_WIDTHS[f"option_{chr(65 + i)}"] = 25


@register("xlsx")
class XlsxExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        split_options = kwargs.get("split_options", True)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".xlsx")

        rows, columns = self.flatten(questions, split_options=split_options)

        wb = Workbook()
        ws = wb.active
        ws.title = "题目"

        # 表头样式
        header_font = Font(bold=True, color="FFFFFF")
        header_fill = PatternFill(start_color="4472C4", end_color="4472C4", fill_type="solid")

        for col_idx, col_key in enumerate(columns, 1):
            cell = ws.cell(
                row=1, column=col_idx,
                value=HEADER_LABELS.get(col_key, col_key),
            )
            cell.font = header_font
            cell.fill = header_fill
            cell.alignment = Alignment(horizontal="center")

        # 数据行
        for row_idx, row in enumerate(rows, 2):
            for col_idx, col_key in enumerate(columns, 1):
                cell = ws.cell(row=row_idx, column=col_idx, value=row.get(col_key, ""))
                cell.alignment = Alignment(wrap_text=True, vertical="top")

        # 列宽
        for col_idx, col_key in enumerate(columns, 1):
            letter = get_column_letter(col_idx)
            ws.column_dimensions[letter].width = COL_WIDTHS.get(col_key, 14)

        # 冻结首行
        ws.freeze_panes = "A2"

        # 自动筛选
        last_col = get_column_letter(len(columns))
        ws.auto_filter.ref = f"A1:{last_col}{len(rows) + 1}"

        wb.save(fp)
        print(f"[INFO] XLSX 导出完成: {fp} ({len(rows)} 行, {len(columns)} 列)")
