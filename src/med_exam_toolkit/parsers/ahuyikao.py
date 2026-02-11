from __future__ import annotations
from med_exam_toolkit.models import Question, SubQuestion
from med_exam_toolkit.parsers import register
from med_exam_toolkit.parsers.base import BaseParser


@register("ahuyikao")
class AhuyikaoParser(BaseParser):

    def can_handle(self, raw: dict) -> bool:
        return raw.get("pkg", "") == "ahuyikao.com"

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
                ))

        elif "A3" in mode or "A4" in mode:
            q.stem = raw.get("stem", "")
            for sq in raw.get("sub_questions", []):
                q.sub_questions.append(SubQuestion(
                    text=sq.get("test", ""),
                    options=sq.get("option", []),
                    answer=sq.get("answer", ""),
                    rate=sq.get("rate", ""),
                    error_prone=sq.get("error_prone", ""),
                    discuss=sq.get("discuss", ""),
                ))

        else:
            # A1 / A2 型题
            q.sub_questions.append(SubQuestion(
                text=raw.get("test", ""),
                options=raw.get("option", []),
                answer=raw.get("answer", ""),
                rate=raw.get("rate", ""),
                error_prone=raw.get("error_prone", ""),
                discuss=raw.get("discuss", ""),
            ))

        return q
