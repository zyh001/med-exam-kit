from __future__ import annotations
import unicodedata
from dataclasses import dataclass, field


def _is_likely_answer(s: str, max_opt: int = 10) -> bool:
    """判断 s 是否为合法的选项字母组合（A 到第 max_opt 个字母，无重复）。"""
    s = s.strip()
    if not s:
        return False
    max_letter = chr(ord('A') + max_opt - 1)
    seen: set[str] = set()
    for ch in s:
        if ch < 'A' or ch > max_letter:
            return False
        if ch in seen:
            return False  # 重复字母不合法（如 'AA'）
        seen.add(ch)
    return True


def _is_likely_discuss(s: str) -> bool:
    """判断 s 是否像解析文字（含汉字/标点/空格，且长度 ≥ 4）。"""
    s = s.strip()
    if len(s) < 4:
        return False
    for ch in s:
        cat = unicodedata.category(ch)
        if cat.startswith('Lo') or cat.startswith('P') or ch == ' ':
            return True
    return False


def sanitize_questions(questions: list) -> int:
    """自动检测并修复答案与解析对调的题目，返回修复数量。
    判定条件：answer 含汉字/标点（像解析），discuss 是纯合法选项字母（如 'A'/'BCE'）。
    支持最多10个选项（A-J），利用实际 options 长度动态限定字母范围。
    """
    fixed = 0
    for q in questions:
        for sq in q.sub_questions:
            ans = (sq.answer or '').strip()
            dis = (sq.discuss or '').strip()
            if not ans or not dis:
                continue
            max_opt = len(sq.options) if sq.options else 10
            if _is_likely_discuss(ans) and _is_likely_answer(dis, max_opt):
                sq.answer, sq.discuss = sq.discuss, sq.answer
                fixed += 1
    return fixed


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
        """有效解析：官方优先，官方为空时用 AI 兜底"""
        return (self.discuss or "").strip() or (self.ai_discuss or "").strip()

    @property
    def answer_source(self) -> str:
        """答案来源：'official' / 'ai' / ''
        - ai_answer 有值且与 eff_answer 一致 → 'ai'（含官方空、或官方与AI相同两种情况）
        - 否则官方有值 → 'official'
        """
        ai  = (self.ai_answer or "").strip()
        eff = self.eff_answer
        if ai and ai == eff:
            return "ai"
        if eff:
            return "official"
        return ""

    @property
    def discuss_source(self) -> str:
        """解析来源：'official' / 'ai' / ''
        - ai_discuss 有值且与 eff_discuss 一致 → 'ai'（含官方空、或官方与AI相同两种情况）
        - 否则官方有值 → 'official'
        """
        ai  = (self.ai_discuss or "").strip()
        eff = self.eff_discuss
        if ai and ai == eff:
            return "ai"
        if eff:
            return "official"
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
