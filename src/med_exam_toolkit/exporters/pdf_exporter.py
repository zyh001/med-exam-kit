from __future__ import annotations
from pathlib import Path
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib.units import mm
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, HRFlowable
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter

_FONT_REGISTERED = False


def _ensure_font():
    global _FONT_REGISTERED
    if _FONT_REGISTERED:
        return
    font_paths = [
        "/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
        "/usr/share/fonts/wqy-microhei/wqy-microhei.ttc",
        "C:/Windows/Fonts/msyh.ttc",
        "/System/Library/Fonts/PingFang.ttc",
    ]
    for fp in font_paths:
        if Path(fp).exists():
            try:
                pdfmetrics.registerFont(TTFont("ChineseFont", fp))
                _FONT_REGISTERED = True
                return
            except Exception:
                continue
    print("[WARN] 未找到中文字体，PDF 中文可能显示异常")


@register("pdf")
class PdfExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".pdf")
        _ensure_font()

        font_name = "ChineseFont" if _FONT_REGISTERED else "Helvetica"

        doc = SimpleDocTemplate(str(fp), pagesize=A4,
                                leftMargin=20 * mm, rightMargin=20 * mm,
                                topMargin=15 * mm, bottomMargin=15 * mm)

        styles = getSampleStyleSheet()
        styles.add(ParagraphStyle(name="CN", fontName=font_name, fontSize=10, leading=14))
        styles.add(ParagraphStyle(name="CNBold", fontName=font_name, fontSize=11, leading=15,
                                  spaceAfter=4))
        styles.add(ParagraphStyle(name="CNSmall", fontName=font_name, fontSize=9, leading=12,
                                  textColor="grey"))

        story = []
        story.append(Paragraph("医学考试题库", ParagraphStyle(
            name="Title", fontName=font_name, fontSize=18, leading=24, alignment=1)))
        story.append(Spacer(1, 10 * mm))

        for idx, q in enumerate(questions, 1):
            # 题目标题
            title = f"第{idx}题 [{q.mode}] {q.unit}"
            story.append(Paragraph(title, styles["CNBold"]))

            if q.stem:
                story.append(Paragraph(f"【题干】{_escape(q.stem)}", styles["CN"]))
                story.append(Spacer(1, 2 * mm))

            if q.shared_options:
                story.append(Paragraph("【共享选项】", styles["CNBold"]))
                for opt in q.shared_options:
                    story.append(Paragraph(f"  {_escape(opt)}", styles["CN"]))
                story.append(Spacer(1, 2 * mm))

            for si, sq in enumerate(q.sub_questions, 1):
                prefix = f"({si}) " if len(q.sub_questions) > 1 else ""
                story.append(Paragraph(f"{prefix}{_escape(sq.text)}", styles["CNBold"]))

                if sq.options and sq.options != q.shared_options:
                    for opt in sq.options:
                        story.append(Paragraph(f"    {_escape(opt)}", styles["CN"]))

                answer_line = f"答案: {sq.answer}"
                if sq.rate:
                    answer_line += f"  正确率: {sq.rate}"
                story.append(Paragraph(answer_line, styles["CN"]))

                discuss = sq.discuss or q.discuss
                if discuss:
                    story.append(Paragraph(f"解析: {_escape(discuss)}", styles["CNSmall"]))

                story.append(Spacer(1, 2 * mm))

            story.append(HRFlowable(width="100%", thickness=0.5, color="grey"))
            story.append(Spacer(1, 4 * mm))

        doc.build(story)
        print(f"[INFO] PDF 导出完成: {fp} ({len(questions)} 题)")


def _escape(text: str) -> str:
    """转义 XML 特殊字符，防止 reportlab 解析出错"""
    return (text
            .replace("&", "&amp;")
            .replace("<", "&lt;")
            .replace(">", "&gt;"))
