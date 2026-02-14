"""组卷 Word 导出"""
from __future__ import annotations
from pathlib import Path
from docx import Document
from docx.shared import Pt, Cm, RGBColor
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.enum.table import WD_TABLE_ALIGNMENT
from docx.oxml.ns import qn
from docx.oxml import OxmlElement
from med_exam_toolkit.models import Question
from med_exam_toolkit.exam.config import ExamConfig


class ExamDocxExporter:

    def __init__(self, config: ExamConfig):
        self.config = config

    def export(self, questions: list[Question], output_path: Path) -> Path:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        fp = output_path.with_suffix(".docx")

        doc = Document()
        self._set_default_font(doc)
        self._add_header(doc, questions)
        self._add_questions(doc, questions)

        if self.config.answer_sheet:
            doc.add_page_break()
            self._add_answer_sheet(doc, questions)

        doc.save(str(fp))
        print(f"[INFO] 试卷导出完成: {fp} ({len(questions)} 题)")
        return fp

    # ── 页面设置 ──
    def _set_font(run, name: str = "宋体", size: Pt | None = None):
        """同时设置中西文字体"""
        run.font.name = name
        r = run._element
        rPr = r.get_or_add_rPr()
        rFonts = rPr.find(qn("w:rFonts"))
        if rFonts is None:
            rFonts = OxmlElement("w:rFonts")
            rPr.insert(0, rFonts)
        rFonts.set(qn("w:eastAsia"), name)
        if size is not None:
            run.font.size = size

    @staticmethod
    def _set_default_font(doc: Document):
        style = doc.styles["Normal"]
        style.font.name = "宋体"
        style.font.size = Pt(10.5)
        # 默认样式也要设东亚字体
        style.element.rPr.rFonts.set(qn("w:eastAsia"), "宋体")
        style.paragraph_format.space_after = Pt(2)
        style.paragraph_format.line_spacing = 1.25

        for section in doc.sections:
            section.top_margin = Cm(2.5)
            section.bottom_margin = Cm(2.5)
            section.left_margin = Cm(2.8)
            section.right_margin = Cm(2.8)

    # ── 试卷头 ──
    def _add_header(self, doc: Document, questions: list[Question]):
        cfg = self.config

        p = doc.add_paragraph()
        p.alignment = WD_ALIGN_PARAGRAPH.CENTER
        run = p.add_run(cfg.title)
        run.bold = True
        run.font.size = Pt(18)

        if cfg.subtitle:
            p = doc.add_paragraph()
            p.alignment = WD_ALIGN_PARAGRAPH.CENTER
            run = p.add_run(cfg.subtitle)
            run.font.size = Pt(12)

        info_parts = []
        if cfg.time_limit:
            info_parts.append(f"考试时间: {cfg.time_limit} 分钟")
        if cfg.total_score:
            info_parts.append(f"满分: {cfg.total_score} 分")
        info_parts.append(f"共 {len(questions)} 题")
        if cfg.score_per_question:
            info_parts.append(f"每题 {cfg.score_per_question} 分")

        p = doc.add_paragraph()
        p.alignment = WD_ALIGN_PARAGRAPH.CENTER
        run = p.add_run("    ".join(info_parts))
        run.font.size = Pt(10)
        run.font.color.rgb = RGBColor(0x66, 0x66, 0x66)

        doc.add_paragraph("━" * 43)

    # ── 题目区 ──
    def _add_questions(self, doc: Document, questions: list[Question]):
        current_mode = ""
        global_idx = 0

        for q in questions:
            if q.mode != current_mode:
                current_mode = q.mode
                p = doc.add_paragraph()
                p.space_before = Pt(12)
                run = p.add_run(f"【{current_mode}】")
                run.bold = True
                run.font.size = Pt(12)

            if len(q.sub_questions) > 1 or q.stem:
                global_idx = self._add_compound_question(doc, q, global_idx)
            else:
                global_idx += 1
                self._add_single_question(doc, q.sub_questions[0], global_idx)

    def _add_single_question(self, doc: Document, sq, idx: int):
        p = doc.add_paragraph()
        run = p.add_run(f"{idx}. {sq.text}")
        run.font.size = Pt(10.5)

        self._add_options(doc, sq.options)

        if self.config.show_answers:
            p = doc.add_paragraph()
            run = p.add_run(f"【答案】{sq.answer}")
            run.font.size = Pt(9)
            run.font.color.rgb = RGBColor(0x00, 0x80, 0x00)

        doc.add_paragraph("")

    def _add_compound_question(self, doc: Document, q: Question, global_idx: int) -> int:
        stem = q.stem or q.raw.get("test", "")
        if stem:
            p = doc.add_paragraph()
            p.space_before = Pt(6)
            run = p.add_run(
                f"（{global_idx + 1}～{global_idx + len(q.sub_questions)} 题共用题干）"
            )
            run.bold = True
            run.font.size = Pt(10)

            p = doc.add_paragraph()
            run = p.add_run(stem)
            run.font.size = Pt(10.5)
            run.italic = True

        if q.shared_options:
            self._add_options(doc, q.shared_options)

        for sq in q.sub_questions:
            global_idx += 1
            p = doc.add_paragraph()
            run = p.add_run(f"{global_idx}. {sq.text}")
            run.font.size = Pt(10.5)

            if not q.shared_options:
                self._add_options(doc, sq.options)

            if self.config.show_answers:
                p = doc.add_paragraph()
                run = p.add_run(f"【答案】{sq.answer}")
                run.font.size = Pt(9)
                run.font.color.rgb = RGBColor(0x00, 0x80, 0x00)

        doc.add_paragraph("")
        return global_idx

    @staticmethod
    def _add_options(doc: Document, options: list[str]):
        if not options:
            return

        max_len = max(len(o) for o in options)

        if max_len <= 20 and len(options) <= 6:
            for i in range(0, len(options), 2):
                left = options[i]
                right = options[i + 1] if i + 1 < len(options) else ""
                p = doc.add_paragraph()
                run = p.add_run(f"    {left:<28s}{right}")
                run.font.size = Pt(10)
                p.paragraph_format.space_after = Pt(0)
                p.paragraph_format.space_before = Pt(0)
        else:
            for opt in options:
                p = doc.add_paragraph()
                run = p.add_run(f"    {opt}")
                run.font.size = Pt(10)
                p.paragraph_format.space_after = Pt(0)
                p.paragraph_format.space_before = Pt(0)

    # ── 答案页 ──
    def _add_answer_sheet(self, doc: Document, questions: list[Question]):
        p = doc.add_paragraph()
        p.alignment = WD_ALIGN_PARAGRAPH.CENTER
        run = p.add_run("参考答案")
        run.bold = True
        run.font.size = Pt(14)

        doc.add_paragraph("")

        cols = 5
        answers = []
        idx = 0
        for q in questions:
            for sq in q.sub_questions:
                idx += 1
                answers.append((idx, sq.answer))

        rows_needed = (len(answers) + cols - 1) // cols
        table = doc.add_table(rows=rows_needed + 1, cols=cols * 2)
        table.alignment = WD_TABLE_ALIGNMENT.CENTER
        table.style = "Table Grid"

        for c in range(cols):
            table.rows[0].cells[c * 2].text = "题号"
            table.rows[0].cells[c * 2 + 1].text = "答案"
            for cell in [table.rows[0].cells[c * 2], table.rows[0].cells[c * 2 + 1]]:
                for paragraph in cell.paragraphs:
                    paragraph.alignment = WD_ALIGN_PARAGRAPH.CENTER
                    for run in paragraph.runs:
                        run.bold = True
                        run.font.size = Pt(9)

        for i, (num, ans) in enumerate(answers):
            row = i // cols + 1
            col = i % cols
            table.rows[row].cells[col * 2].text = str(num)
            table.rows[row].cells[col * 2 + 1].text = ans
            for cell in [table.rows[row].cells[col * 2], table.rows[row].cells[col * 2 + 1]]:
                for paragraph in cell.paragraphs:
                    paragraph.alignment = WD_ALIGN_PARAGRAPH.CENTER
                    for run in paragraph.runs:
                        run.font.size = Pt(9)

        if self.config.show_discuss:
            doc.add_paragraph("")
            p = doc.add_paragraph()
            run = p.add_run("题目解析")
            run.bold = True
            run.font.size = Pt(12)

            idx = 0
            for q in questions:
                for sq in q.sub_questions:
                    idx += 1
                    if sq.discuss:
                        p = doc.add_paragraph()
                        run = p.add_run(f"{idx}. 【{sq.answer}】")
                        run.bold = True
                        run.font.size = Pt(9)
                        run = p.add_run(f" {sq.discuss}")
                        run.font.size = Pt(9)
                        run.font.color.rgb = RGBColor(0x55, 0x55, 0x55)
