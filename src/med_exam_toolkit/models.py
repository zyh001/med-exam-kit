from __future__ import annotations
from dataclasses import dataclass, field


@dataclass
class SubQuestion:
    """单个小题（A1/A2 整题也视为一个 SubQuestion）"""
    text: str
    options: list[str]
    answer: str
    rate: str = ""
    error_prone: str = ""
    discuss: str = ""
    point: str = ""
    # AI 补全结果（默认不覆盖正式字段）
    ai_answer: str = ""
    ai_discuss: str = ""
    ai_confidence: float = 0.0
    ai_model: str = ""
    ai_status: str = ""  # pending/accepted/rejected

    @property
    def eff_answer(self) -> str:
        """有效答案：优先用正式字段，为空时用 AI 补全结果"""
        return (self.answer or "").strip() or (self.ai_answer or "").strip()

    @property
    def eff_discuss(self) -> str:
        """有效解析：优先用正式字段，为空时用 AI 补全结果"""
        return (self.discuss or "").strip() or (self.ai_discuss or "").strip()

    @property
    def answer_source(self) -> str:
        """答案来源：'official' / 'ai' / ''"""
        if (self.answer or "").strip():
            return "official"
        if (self.ai_answer or "").strip():
            return "ai"
        return ""

    @property
    def discuss_source(self) -> str:
        """解析来源：'official' / 'ai' / ''"""
        if (self.discuss or "").strip():
            return "official"
        if (self.ai_discuss or "").strip():
            return "ai"
        return ""


@dataclass
class Question:
    """统一题目模型，所有 parser 输出都归一化到此结构"""
    fingerprint: str = ""                    # 去重指纹，由 dedup 模块填充
    name: str = ""                           # 原始文件名/时间戳
    pkg: str = ""                            # 来源 app
    cls: str = ""                            # 题库分类
    unit: str = ""                           # 章节
    mode: str = ""                           # 题型：A1, A2, A3/A4, B1 ...
    stem: str = ""                           # 题干（A3/A4 共享题干；B 型为空）
    shared_options: list[str] = field(default_factory=list)  # B 型共享选项
    sub_questions: list[SubQuestion] = field(default_factory=list)
    discuss: str = ""                        # 整题解析
    source_file: str = ""
    raw: dict = field(default_factory=dict)  # 保留原始 JSON
