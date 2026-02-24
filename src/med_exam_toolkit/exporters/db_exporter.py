from __future__ import annotations
import json
from pathlib import Path
from sqlalchemy import create_engine, Column, String, Text, Integer, MetaData, Table
from sqlalchemy.orm import Session
from med_exam_toolkit.models import Question
from med_exam_toolkit.exporters import register
from med_exam_toolkit.exporters.base import BaseExporter


def _build_table(metadata: MetaData) -> Table:
    return Table(
        "questions", metadata,
        Column("id",             Integer, primary_key=True, autoincrement=True),
        Column("fingerprint",    String(32), unique=True, index=True),
        Column("pkg",            String(128)),
        Column("cls",            String(128)),
        Column("unit",           String(256)),
        Column("mode",           String(32)),
        Column("stem",           Text, default=""),
        Column("sub_index",      Integer),
        Column("text",           Text),
        Column("options",        Text),
        Column("answer",         String(16)),
        Column("answer_source",  String(16)),
        Column("rate",           String(16)),
        Column("error_prone",    String(16)),
        Column("discuss",        Text),
        Column("discuss_source", String(16)),
        Column("point",          Text),
        Column("raw_json",       Text),
    )


@register("db")
class DbExporter(BaseExporter):

    def export(self, questions: list[Question], output_path: Path, **kwargs) -> None:
        db_url = kwargs.get("db_url", f"sqlite:///{output_path.with_suffix('.db')}")
        engine   = create_engine(db_url, echo=False)
        metadata = MetaData()
        table    = _build_table(metadata)
        metadata.create_all(engine)

        rows = []
        for q in questions:
            for i, sq in enumerate(q.sub_questions, 1):
                rows.append({
                    "fingerprint":    f"{q.fingerprint}_{i}",
                    "pkg":            q.pkg,
                    "cls":            q.cls,
                    "unit":           q.unit,
                    "mode":           q.mode,
                    "stem":           q.stem,
                    "sub_index":      i,
                    "text":           sq.text,
                    "options":        json.dumps(sq.options, ensure_ascii=False),
                    # ← eff_answer / eff_discuss 兜底
                    "answer":         sq.eff_answer,
                    "answer_source":  sq.answer_source,
                    "rate":           sq.rate,
                    "error_prone":    sq.error_prone,
                    "discuss":        sq.eff_discuss,
                    "discuss_source": sq.discuss_source,
                    "point":          sq.point,
                    "raw_json":       json.dumps(q.raw, ensure_ascii=False),
                })

        with Session(engine) as session:
            conn = session.connection()
            existing = {row.fingerprint for row in conn.execute(table.select())}
            new_rows = [r for r in rows if r["fingerprint"] not in existing]
            if new_rows:
                conn.execute(table.insert(), new_rows)
            session.commit()

        print(f"[INFO] 数据库导出完成: {db_url} (新增 {len(new_rows)}/{len(rows)} 行)")
