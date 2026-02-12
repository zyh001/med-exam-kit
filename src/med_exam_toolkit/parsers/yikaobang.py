from __future__ import annotations
from med_exam_toolkit.models import Question, SubQuestion
from med_exam_toolkit.parsers import register
from med_exam_toolkit.parsers.base import BaseParser


@register("yikaobang")
class YikaobangParser(BaseParser):
    """解析 com.yikaobang.yixue 的 JSON 格式"""

    def can_handle(self, raw: dict) -> bool:
        return "yikaobang" in raw.get("pkg", "")

    def parse(self, raw: dict) -> Question:
        mode = raw.get("mode", "")
        q = Question(
            name=raw.get("name", ""),
            pkg=raw.get("pkg", ""),
            cls=raw.get("cls", ""),
            unit=raw.get("unit", ""),
            mode=mode,
            raw=raw,
        )

        if "B" in mode and "型题" in mode:
            q.shared_options = raw.get("shared_options", [])
            q.discuss = raw.get("discuss", "")
            for sq in raw.get("sub_questions", []):
                q.sub_questions.append(SubQuestion(
                    text=sq.get("test", ""),
                    options=q.shared_options,
                    answer=sq.get("answer", ""),
                    rate=sq.get("rate", ""),
                    error_prone=sq.get("error_prone", ""),
                    discuss=sq.get("discuss", ""),
                    point=sq.get("point", ""),
                ))

        elif "A3" in mode or "A4" in mode:
            q.stem = raw.get("test", "")
            for sq in raw.get("sub_questions", []):
                q.sub_questions.append(SubQuestion(
                    text=sq.get("sub_test", ""),
                    options=sq.get("option", []),
                    answer=sq.get("answer", ""),
                    rate=sq.get("rate", ""),
                    error_prone=sq.get("error_prone", ""),
                    discuss=sq.get("discuss", ""),
                    point=sq.get("point", ""),
                ))

        else:
            # A1 / A2 —— yikaobang 多了 point 字段
            q.sub_questions.append(SubQuestion(
                text=raw.get("test", ""),
                options=raw.get("option", []),
                answer=raw.get("answer", ""),
                rate=raw.get("rate", ""),
                error_prone=raw.get("error_prone", ""),
                discuss=raw.get("discuss", ""),
                point=raw.get("point", ""),
            ))

        return q
