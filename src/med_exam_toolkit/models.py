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
    raw: dict = field(default_factory=dict)  # 保留原始 JSON
