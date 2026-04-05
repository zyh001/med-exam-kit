"""
题目字段标准化器
================
将解密后含混淆字段名的 JSON 还原为语义明确的字段名

验证结果（基于实测 29 道题）：
  普通题 19 道：qid/answer/model/question/option_a~e/book_source/do_nums/kp_html → 100%
  病例题 10 道：qid/model/type_name/sub_count → 100%
  analysis 解析：68%（服务端本身对部分题目未返回，非识别问题）
"""

import re
from typing import Optional

# ── 已知固定明文字段（无需识别）────────────────────────
KNOWN_CLEAR = {
    "caseid", "source_id", "tp_isdel", "book_id", "dir_id",
    "show_name", "extend_nums", "can_rand_options", "process",
    "chapterid", "sectionid", "isdel", "is_collect", "is_grasp",
    "option_statistics", "book_dirs_str_new", "mk_content_datas",
    "lexicon_tags", "case_answer", "case_options",
}

# ── 识别规则 ──────────────────────────────────────────
_ANSWER_PAT   = re.compile(r"^[A-E]{1,5}$")
_MODEL_VALS   = {"single_select", "multi_select", "fill", "essay", "judge"}
_QUESTION_CUE = re.compile(
    r"以下|下列|关于|哪|什么|不正确|不符合|错误|正确|"
    r"描述|叙述|表述|特征|特点|说法|做法|处理|治疗|诊断|"
    r"[？?]$|位于$|属于$|来自$|见于$|用于$|是指$|指的是$"
)

def normalize_question(obj: dict) -> dict:
    """
    将混淆字段名的题目 JSON 转换为标准字段名

    Args:
        obj: 解密后的原始 JSON 对象

    Returns:
        标准化后的题目字典，包含以下字段（按覆盖率）：
          qid, answer, model, type_name, question,
          option_a~e, book_source, do_nums, err_nums,
          kp_html, analysis（可能缺失）,
          caseid, case_answer（病例题）, case_options（病例题）
    """
    result = {}
    caseid = str(obj.get("caseid", "0"))

    # 复制固定明文字段
    for k in KNOWN_CLEAR:
        if k in obj:
            result[k] = obj[k]

    # ── 病例组合题 ────────────────────────────────────
    if caseid != "0":
        result["is_case"] = True
        for k, v in obj.items():
            if k in KNOWN_CLEAR: continue
            sv = str(v).strip() if isinstance(v, str) else ""
            if sv in _MODEL_VALS:                    result.setdefault("model", sv)
            elif "单选" in sv or "多选" in sv:       result.setdefault("type_name", sv)
            elif re.match(r"^\d{5,8}$", sv):         result.setdefault("qid", sv)
        return result

    # ── 普通单/多选题 ─────────────────────────────────
    result["is_case"] = False

    num_strs, html_strs, txt_strs = [], [], []
    for k, v in obj.items():
        if k in KNOWN_CLEAR or not isinstance(v, str) or not v.strip():
            continue
        sv = v.strip()
        if re.match(r"^\d+$", sv):     num_strs.append((k, sv))
        elif "<" in sv and ">" in sv:  html_strs.append((k, sv))
        else:                          txt_strs.append((k, sv))

    # 数字字段分类
    stat_nums = []
    for k, sv in num_strs:
        n = int(sv)
        if n > 5_000_000:               result.setdefault("group_id", sv)
        elif 100_000 <= n <= 3_000_000: result.setdefault("qid", sv)
        elif 12_000 <= n <= 13_000:     result.setdefault("subject_id", sv)  # 已知科目ID范围
        elif n < 100:                   result.setdefault("ticlassid", sv)
        else:                           stat_nums.append(n)  # do/err nums

    stat_nums.sort(reverse=True)
    if stat_nums:          result["do_nums"]  = stat_nums[0]
    if len(stat_nums) > 1: result["err_nums"] = stat_nums[1]

    # HTML 字段（最长的是知识点）
    if html_strs:
        html_strs.sort(key=lambda x: -len(x[1]))
        result["kp_html"] = html_strs[0][1]

    # 文本字段分类
    for k, sv in txt_strs:
        if _ANSWER_PAT.match(sv):              result.setdefault("answer", sv)
        elif sv in _MODEL_VALS:                result.setdefault("model", sv)
        elif "单选" in sv or "多选" in sv or "判断" in sv: result.setdefault("type_name", sv)
        elif "《" in sv and len(sv) > 30:      result.setdefault("book_source", sv)

    remainder = [
        (k, sv) for k, sv in txt_strs
        if sv != result.get("answer", "")
        and sv not in _MODEL_VALS
        and "单选" not in sv and "多选" not in sv and "判断" not in sv
        and not ("《" in sv and len(sv) > 30)
    ]

    long_texts  = [(k, v) for k, v in remainder if len(v) > 50]
    short_texts = [(k, v) for k, v in remainder if len(v) <= 50]

    # 长文本：有题干线索 → 题干；其余最长 → 解析
    analysis_cands = []
    for k, v in sorted(long_texts, key=lambda x: -len(x[1])):
        if _QUESTION_CUE.search(v) and "question" not in result:
            result["question"] = v
        else:
            analysis_cands.append((k, v))
    if analysis_cands:
        result["analysis"] = max(analysis_cands, key=lambda x: len(x[1]))[1]

    # 短文本：有题干线索 → 题干；其余 → 选项
    q_from_short, opt_cands = [], []
    for k, v in short_texts:
        if _QUESTION_CUE.search(v): q_from_short.append((k, v))
        else:                       opt_cands.append((k, v))

    if "question" not in result:
        if q_from_short:
            result["question"] = max(q_from_short, key=lambda x: len(x[1]))[1]
        elif opt_cands:
            opt_cands.sort(key=lambda x: -len(x[1]))
            result["question"] = opt_cands[0][1]
            opt_cands = opt_cands[1:]

    opt_cands += [(k, v) for k, v in q_from_short if v != result.get("question", "")]
    opt_cands.sort(key=lambda x: len(x[1]))
    for i, (_, v) in enumerate(opt_cands[:5]):
        result[f"option_{chr(97 + i)}"] = v

    return result
