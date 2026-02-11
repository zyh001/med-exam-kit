from __future__ import annotations
from abc import ABC, abstractmethod
from med_exam_toolkit.models import Question


class BaseParser(ABC):
    """
    解析器基类。
    每个 app 的 JSON 格式不同，子类负责将原始 dict 归一化为 Question。
    """

    @abstractmethod
    def parse(self, raw: dict) -> Question:
        ...

    def can_handle(self, raw: dict) -> bool:
        """可选：自动探测是否能处理该 JSON"""
        return False
