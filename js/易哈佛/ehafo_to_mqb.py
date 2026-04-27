#!/usr/bin/env python3
"""
ehafo.db → .mqb 转换器
======================
读取 ehafo 爬虫生成的 SQLite 数据库，将题目转换为 med-exam-kit 的 MQB2 格式。

用法：
    python ehafo_to_mqb.py                          # 默认读 ehafo.db → ehafo.mqb
    python ehafo_to_mqb.py --db ehafo.db -o output  # 指定路径
    python ehafo_to_mqb.py --password 123456         # 加密输出
    python ehafo_to_mqb.py --split-by-subject        # 按科目拆分
    python ehafo_to_mqb.py --dry-run                 # 仅统计，不写文件

数据映射：
    ehafo show_name / type_name          →  med-exam-kit mode
    ────────────────────────────────────────────────────────
    A1单选题                             →  A1型题
    A2单选题                             →  A2型题
    A3/A4 型题 (caseid != 0, case 模式)  →  A3/A4型题
    B1 型题   (caseid != 0, 共享选项)    →  B1型题
    其余 single_select                   →  A1型题 (fallback)
    其余 multi_select                    →  A2型题 (fallback)
"""

from __future__ import annotations

import argparse
import dataclasses
import hashlib
import json
import logging
import os
import re
import sqlite3
import sys
import time
import zlib
from collections import defaultdict
from pathlib import Path
from typing import Any, Optional

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
)
log = logging.getLogger(__name__)


# ════════════════════════════════════════════════════════════
#   Question / SubQuestion 模型（与 med-exam-kit 完全兼容）
# ════════════════════════════════════════════════════════════

@dataclasses.dataclass
class SubQuestion:
    text: str
    options: list[str]
    answer: str
    rate: str = ""
    error_prone: str = ""
    discuss: str = ""
    point: str = ""
    ai_answer: str = ""
    ai_discuss: str = ""
    ai_confidence: float = 0.0
    ai_model: str = ""
    ai_status: str = ""


@dataclasses.dataclass
class Question:
    fingerprint: str = ""
    name: str = ""
    pkg: str = ""
    cls: str = ""
    unit: str = ""
    mode: str = ""
    stem: str = ""
    shared_options: list[str] = dataclasses.field(default_factory=list)
    sub_questions: list[SubQuestion] = dataclasses.field(default_factory=list)
    discuss: str = ""
    source_file: str = ""
    raw: dict = dataclasses.field(default_factory=dict)


# ════════════════════════════════════════════════════════════
#   MQB2 文件写入（复刻 bank.py 逻辑，无外部依赖）
# ════════════════════════════════════════════════════════════

MAGIC_V2 = b"MQB2"

try:
    from cryptography.fernet import Fernet
    import base64

    def _derive_key(password: str, salt: bytes) -> bytes:
        dk = hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 100_000)
        return base64.urlsafe_b64encode(dk)

    HAS_CRYPTO = True
except ImportError:
    HAS_CRYPTO = False


def _sq_to_dict(sq: SubQuestion) -> dict:
    return dataclasses.asdict(sq)


def _q_to_dict(q: Question) -> dict:
    d = dataclasses.asdict(q)
    d["raw"] = {}
    d["sub_questions"] = [dataclasses.asdict(sq) for sq in q.sub_questions]
    return d


def save_mqb(
    questions: list[Question],
    output: Path,
    password: str | None = None,
    compress: bool = True,
) -> Path:
    """写入 MQB2 文件，与 med-exam-kit bank.py 格式完全兼容。"""
    fp = output.with_suffix(".mqb")
    fp.parent.mkdir(parents=True, exist_ok=True)

    payload = json.dumps(
        [_q_to_dict(q) for q in questions],
        ensure_ascii=False,
    ).encode("utf-8")
    raw_size = len(payload)

    if compress:
        payload = zlib.compress(payload, level=6)

    salt = os.urandom(16)
    meta: dict[str, Any] = {
        "count": len(questions),
        "created": time.time(),
        "encrypted": password is not None,
        "compressed": compress,
        "salt_hex": salt.hex(),
    }

    if password:
        if not HAS_CRYPTO:
            raise ImportError("加密需要 cryptography: pip install cryptography")
        key = _derive_key(password, salt)
        payload = Fernet(key).encrypt(payload)

    meta_bytes = json.dumps(meta, ensure_ascii=False).encode("utf-8")

    with open(fp, "wb") as fh:
        fh.write(MAGIC_V2)
        fh.write(len(meta_bytes).to_bytes(4, "big"))
        fh.write(meta_bytes)
        fh.write(payload)

    compressed_size = len(payload)
    ratio = (1 - compressed_size / raw_size) * 100 if raw_size else 0
    log.info(f"已写入: {fp} ({len(questions)} 题, {compressed_size:,} B, 压缩率 {ratio:.1f}%)")
    return fp


# ════════════════════════════════════════════════════════════
#   ehafo 数据库 → Question 模型 映射
# ════════════════════════════════════════════════════════════

PKG_NAME = "com.ehafo"

# show_name / type_name → med-exam-kit mode
MODE_MAP = {
    "A1单选题": "A1型题",
    "A1/A2单选题": "A1型题",
    "A2单选题": "A2型题",
    "A3单选题": "A3/A4型题",
    "A3/A4单选题": "A3/A4型题",
    "A3/A4型题": "A3/A4型题",
    "B1单选题": "B1型题",
    "B1型题": "B1型题",
    "多选题": "X型题",
}


def _detect_mode(type_name: str, model: str, caseid: str) -> str:
    """从 type_name / model / caseid 推断 med-exam-kit 的 mode。"""
    tn = (type_name or "").strip()

    # 精确匹配
    for key, mode in MODE_MAP.items():
        if key in tn:
            return mode

    # 通过 caseid 推断
    if caseid and caseid != "0":
        # 有 caseid 的题目通常是 B1（共享选项）或 A3/A4（共享题干）
        # 具体判断需要看是否有 case_options
        return "B1型题"

    # fallback: 按 model
    if model == "multi_select":
        return "X型题"
    return "A1型题"


def _collect_options(row: dict) -> list[str]:
    """从 DB 行收集非空选项列表。"""
    opts = []
    for letter in "abcde":
        v = (row.get(f"option_{letter}") or "").strip()
        if v:
            opts.append(v)
    return opts


def _build_cls_unit(row: dict, subject_map: dict, chapter_map: dict) -> tuple[str, str]:
    """构建分类和章节层级名称。"""
    sid = row.get("subject_id") or ""
    sec_id = row.get("section_id") or ""

    cls_name = subject_map.get(sid, "")

    # 通过 section → chapter → subject 构建层级
    unit_name = ""
    if sec_id and sec_id in chapter_map:
        unit_name = chapter_map[sec_id]

    return cls_name, unit_name


def _calc_rate(do_nums: int, err_nums: int) -> str:
    """计算正确率字符串。"""
    if do_nums <= 0:
        return ""
    correct = do_nums - err_nums
    pct = correct / do_nums * 100
    return f"{pct:.0f}%"


def convert_normal_question(row: dict, subject_map: dict, chapter_map: dict) -> Question:
    """转换普通题（caseid=0）为 Question 对象。"""
    type_name = row.get("type_name") or ""
    model = row.get("model") or ""
    caseid = str(row.get("caseid") or "0")
    mode = _detect_mode(type_name, model, caseid)

    cls_name, unit_name = _build_cls_unit(row, subject_map, chapter_map)

    options = _collect_options(row)
    question_text = (row.get("question") or "").strip()
    answer = (row.get("answer") or "").strip()
    analysis = (row.get("analysis") or "").strip()
    do_nums = int(row.get("do_nums") or 0)
    err_nums = int(row.get("err_nums") or 0)
    rate = _calc_rate(do_nums, err_nums)
    kp = (row.get("kp_name") or "").strip()

    q = Question(
        name=row.get("qid") or "",
        pkg=PKG_NAME,
        cls=cls_name,
        unit=unit_name,
        mode=mode,
    )
    q.sub_questions.append(SubQuestion(
        text=question_text,
        options=options,
        answer=answer,
        rate=rate,
        discuss=analysis,
        point=kp,
    ))
    return q


def convert_case_group(
    rows: list[dict],
    raw_jsons: dict[str, dict],
    subject_map: dict,
    chapter_map: dict,
) -> list[Question]:
    """
    转换病例组合题组（同一 caseid 的多行）为 Question 对象。

    病例题有两种形态：
    1. A3/A4 型（共享题干 + 独立选项）— 多道子题共用一段病例描述
    2. B1 型（共享选项 + 独立题干）— 多道子题共用一组选项

    若 raw_json 中有 case_options / case_answer，使用结构化数据；
    否则退化为"每行一道普通题"（信息有损但不丢题）。
    """
    results = []
    if not rows:
        return results

    first = rows[0]
    caseid = str(first.get("caseid") or "0")
    cls_name, unit_name = _build_cls_unit(first, subject_map, chapter_map)

    # 尝试从 raw_json 获取结构化数据
    raw = raw_jsons.get(caseid, {})
    case_options_dict = raw.get("case_options", {})
    case_answer_list = raw.get("case_answer", [])

    if case_options_dict and case_answer_list:
        # ── 有完整的病例结构数据（B1 型：共享选项）──
        shared_opts = []
        opt_keys = sorted(case_options_dict.keys())  # A, B, C, D, E
        for k in opt_keys:
            shared_opts.append(case_options_dict[k])

        type_name = raw.get("type_name") or raw.get("show_name") or ""
        if "A3" in type_name or "A4" in type_name:
            mode = "A3/A4型题"
        else:
            mode = "B1型题"

        q = Question(
            name=f"case_{caseid}",
            pkg=PKG_NAME,
            cls=cls_name,
            unit=unit_name,
            mode=mode,
            shared_options=shared_opts,
        )

        # 如果是 A3/A4，第一道的 question 可能是共享题干
        main_question = (first.get("question") or "").strip()
        if mode == "A3/A4型题" and main_question:
            q.stem = main_question

        for ca in case_answer_list:
            sub_answer = (ca.get("answer") or "").strip()
            sub_analysis = (ca.get("analysis") or "").strip()
            sub_text = (ca.get("question") or ca.get("text") or "").strip()

            # B1 型子题可能有独立题干（从对应 DB 行中取）
            if not sub_text:
                sub_id = str(ca.get("id") or "")
                for r in rows:
                    if str(r.get("qid") or "") == sub_id:
                        sub_text = (r.get("question") or "").strip()
                        break

            q.sub_questions.append(SubQuestion(
                text=sub_text,
                options=shared_opts,
                answer=sub_answer,
                discuss=sub_analysis,
            ))

        if q.sub_questions:
            results.append(q)
        return results

    # ── 无结构化数据，退化为逐行转换 ──
    # 尝试检测是否为 A3/A4（共享题干）
    questions_text = [(r.get("question") or "").strip() for r in rows]
    options_per_row = [_collect_options(r) for r in rows]

    # 如果所有子题选项相同 → B1 型
    all_same_opts = (
        len(set(tuple(o) for o in options_per_row if o)) == 1
        and any(options_per_row)
    )

    if all_same_opts and len(rows) >= 2:
        shared_opts = options_per_row[0]
        mode = "B1型题"
        q = Question(
            name=f"case_{caseid}",
            pkg=PKG_NAME,
            cls=cls_name,
            unit=unit_name,
            mode=mode,
            shared_options=shared_opts,
        )
        for r in rows:
            do_nums = int(r.get("do_nums") or 0)
            err_nums = int(r.get("err_nums") or 0)
            q.sub_questions.append(SubQuestion(
                text=(r.get("question") or "").strip(),
                options=shared_opts,
                answer=(r.get("answer") or "").strip(),
                rate=_calc_rate(do_nums, err_nums),
                discuss=(r.get("analysis") or "").strip(),
                point=(r.get("kp_name") or "").strip(),
            ))
        if q.sub_questions:
            results.append(q)
    else:
        # 退化为多道独立题
        for r in rows:
            results.append(convert_normal_question(r, subject_map, chapter_map))

    return results


def load_from_db(db_path: str) -> list[Question]:
    """
    从 ehafo.db 读取全部题目并转换为 Question 列表。
    """
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row

    # ── 构建辅助映射 ──
    subject_map = {}
    try:
        for r in conn.execute("SELECT id, name FROM subjects"):
            subject_map[r["id"]] = r["name"]
    except sqlite3.OperationalError:
        log.warning("subjects 表不存在或为空")

    chapter_map = {}  # section_id → "章名 / 节名"
    try:
        rows = conn.execute("""
            SELECT s.id AS sec_id, c.name AS chap_name, s.name AS sec_name
            FROM sections s
            LEFT JOIN chapters c ON s.chapter_id = c.id
        """).fetchall()
        for r in rows:
            parts = []
            if r["chap_name"]:
                parts.append(r["chap_name"])
            if r["sec_name"]:
                parts.append(r["sec_name"])
            chapter_map[r["sec_id"]] = " / ".join(parts) if parts else ""
    except sqlite3.OperationalError:
        log.warning("sections/chapters 表不存在或为空")

    # ── 读取 raw_json（若有此列）──
    has_raw_json = False
    try:
        conn.execute("SELECT raw_json FROM questions LIMIT 1")
        has_raw_json = True
    except sqlite3.OperationalError:
        pass

    raw_jsons: dict[str, dict] = {}  # caseid → first raw_json with case_answer
    if has_raw_json:
        cursor = conn.execute(
            "SELECT caseid, raw_json FROM questions "
            "WHERE caseid != '0' AND raw_json IS NOT NULL AND raw_json != ''"
        )
        for r in cursor:
            cid = str(r["caseid"])
            if cid not in raw_jsons:
                try:
                    obj = json.loads(r["raw_json"])
                    if obj.get("case_answer") or obj.get("case_options"):
                        raw_jsons[cid] = obj
                except (json.JSONDecodeError, TypeError):
                    pass

    # ── 读取全部题目 ──
    all_rows = conn.execute(
        "SELECT * FROM questions ORDER BY subject_id, section_id, qid"
    ).fetchall()
    conn.close()

    # 转为 dict 列表以便操作
    all_dicts = [dict(r) for r in all_rows]
    log.info(f"从数据库读取 {len(all_dicts)} 道题目")

    # ── 按 caseid 分组 ──
    normal_rows = []
    case_groups: dict[str, list[dict]] = defaultdict(list)

    for d in all_dicts:
        caseid = str(d.get("caseid") or "0")
        if caseid == "0":
            normal_rows.append(d)
        else:
            case_groups[caseid].append(d)

    # ── 转换普通题 ──
    questions: list[Question] = []
    for r in normal_rows:
        questions.append(convert_normal_question(r, subject_map, chapter_map))

    # ── 转换病例组合题 ──
    for caseid, group in case_groups.items():
        questions.extend(convert_case_group(group, raw_jsons, subject_map, chapter_map))

    log.info(
        f"转换完成: {len(questions)} 题 "
        f"(普通 {len(normal_rows)}, 病例组 {len(case_groups)} 组)"
    )

    total_sq = sum(len(q.sub_questions) for q in questions)
    log.info(f"总子题数: {total_sq}")

    return questions


# ════════════════════════════════════════════════════════════
#   统计报告
# ════════════════════════════════════════════════════════════

def print_stats(questions: list[Question]):
    print("\n" + "═" * 60)
    print("📊  MQB 转换统计")
    print("═" * 60)

    total_sq = sum(len(q.sub_questions) for q in questions)
    print(f"  题目数: {len(questions)}    子题总数: {total_sq}")

    # 按 mode 统计
    mode_counter: dict[str, int] = defaultdict(int)
    for q in questions:
        mode_counter[q.mode] += 1
    print("\n  题型分布:")
    for mode, count in sorted(mode_counter.items(), key=lambda x: -x[1]):
        print(f"    {mode:16s}  {count} 题")

    # 按 cls（科目）统计
    cls_counter: dict[str, int] = defaultdict(int)
    for q in questions:
        cls_counter[q.cls or "(未分类)"] += 1
    if cls_counter:
        print("\n  科目分布:")
        for cls_name, count in sorted(cls_counter.items(), key=lambda x: -x[1]):
            print(f"    {cls_name[:36]:36s}  {count} 题")

    # 答案 / 解析覆盖率
    has_answer = sum(
        1 for q in questions
        for sq in q.sub_questions if sq.answer
    )
    has_discuss = sum(
        1 for q in questions
        for sq in q.sub_questions if sq.discuss
    )
    print(f"\n  有答案的子题: {has_answer} / {total_sq}")
    print(f"  有解析的子题: {has_discuss} / {total_sq}")
    print("═" * 60)


# ════════════════════════════════════════════════════════════
#   命令行入口
# ════════════════════════════════════════════════════════════

def main():
    ap = argparse.ArgumentParser(
        description="ehafo.db → .mqb 转换器 (med-exam-kit 格式)"
    )
    ap.add_argument(
        "--db", default="ehafo.db",
        help="ehafo 爬虫数据库路径 (默认 ehafo.db)"
    )
    ap.add_argument(
        "-o", "--output", default="ehafo",
        help="输出 .mqb 文件路径 (默认 ehafo.mqb)"
    )
    ap.add_argument(
        "--password", default=None,
        help="加密密码 (可选, 需要 cryptography 库)"
    )
    ap.add_argument(
        "--split-by-subject", action="store_true",
        help="按科目拆分输出多个 .mqb 文件"
    )
    ap.add_argument(
        "--dry-run", action="store_true",
        help="仅统计，不写出文件"
    )
    ap.add_argument(
        "--no-compress", action="store_true",
        help="不压缩 (调试用)"
    )
    args = ap.parse_args()

    if not Path(args.db).exists():
        log.error(f"数据库不存在: {args.db}")
        log.error("请先运行爬虫: python scraper/run.py")
        sys.exit(1)

    questions = load_from_db(args.db)
    if not questions:
        log.error("数据库中无题目，请先运行爬虫")
        sys.exit(1)

    print_stats(questions)

    if args.dry_run:
        log.info("--dry-run 模式，未写出文件")
        return

    compress = not args.no_compress

    if args.split_by_subject:
        # 按科目拆分
        by_cls: dict[str, list[Question]] = defaultdict(list)
        for q in questions:
            by_cls[q.cls or "未分类"].append(q)

        output_dir = Path(args.output)
        output_dir.mkdir(parents=True, exist_ok=True)

        for cls_name, qs in by_cls.items():
            safe_name = re.sub(r'[\\/:*?"<>|]', '_', cls_name)
            out_path = output_dir / safe_name
            save_mqb(qs, out_path, password=args.password, compress=compress)
    else:
        save_mqb(questions, Path(args.output), password=args.password, compress=compress)

    log.info("✅ 全部完成！")
    log.info(f"使用方式: med-exam quiz {args.output}.mqb")


if __name__ == "__main__":
    main()
