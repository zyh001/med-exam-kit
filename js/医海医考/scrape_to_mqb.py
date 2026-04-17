#!/usr/bin/env python3
"""
医海医考 → MQB 题库爬取脚本

从医海医考 API 爬取全部题目，转换为 med-exam-kit 可识别  .mqb 格式。
正确处理 A1/A2型题、A3/A4型题（共用题干）、案例分析题（共用题干）。
自动计算题目指纹用于去重。

图片处理:
  API 在三个独立字段中提供图片 URL (title/analysis/stem 字段本身是纯文本,
  不含内联 <img> 标签):
    - image            → 题干图片
    - analysis_image   → 解析图片
    - topic_stem.image → 共用题干图片
  本脚本会以 <img src="..."> HTML 标签形式追加到对应文本末尾,
  方便前端直接 innerHTML 渲染.

用法:
    cd med-exam-kit/js/医海医考
    pip install pycryptodome requests
    python scrape_to_mqb.py

生成的 .mqb 文件可直接用于 med-exam quiz 刷题:
    med-exam quiz --bank 医海医考_口腔全科.mqb
"""

import sys
import os
import time
import json
import re
import zlib
import hashlib
import logging
from datetime import datetime
from pathlib import Path
from collections import OrderedDict
from dataclasses import dataclass, field, asdict

# ── 路径设置 ──
SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))
PROJECT_ROOT = SCRIPT_DIR.parent.parent
sys.path.insert(0, str(PROJECT_ROOT / "src"))

from yhyk_sdk import YhykClient

# ── 实际包名 (Frida attach 时用的 Android applicationId) ──
PKG_NAME = "uni.UNI7EE4208"

# ── 尝试导入 med_exam_toolkit ──
try:
    from med_exam_toolkit.bank import save_bank
    from med_exam_toolkit.models import Question, SubQuestion
    from med_exam_toolkit.dedup import compute_fingerprint
    HAS_TOOLKIT = True
except ImportError:
    HAS_TOOLKIT = False

# ═══════════════════════════════════════════════════════════════
# 内联 fallback（med_exam_toolkit 不可用时）
# ═══════════════════════════════════════════════════════════════

if not HAS_TOOLKIT:

    @dataclass
    class SubQuestion:
        text: str = ""
        options: list = field(default_factory=list)
        answer: str = ""
        rate: str = ""
        error_prone: str = ""
        discuss: str = ""
        point: str = ""
        ai_answer: str = ""
        ai_discuss: str = ""
        ai_confidence: float = 0.0
        ai_model: str = ""
        ai_status: str = ""

    @dataclass
    class Question:
        fingerprint: str = ""
        name: str = ""
        pkg: str = ""
        cls: str = ""
        unit: str = ""
        mode: str = ""
        stem: str = ""
        shared_options: list = field(default_factory=list)
        sub_questions: list = field(default_factory=list)
        discuss: str = ""
        source_file: str = ""
        raw: dict = field(default_factory=dict)

    def _normalize_text(text: str) -> str:
        text = re.sub(r"\s+", "", text)
        text = text.replace("，", ",").replace("。", ".").replace("；", ";")
        text = text.replace("：", ":").replace("（", "(").replace("）", ")")
        return text.lower()

    def _resolve_answer_text(sq: SubQuestion) -> str:
        try:
            idx = ord(sq.answer.strip().upper()) - ord("A")
            if 0 <= idx < len(sq.options):
                return _normalize_text(sq.options[idx])
        except (TypeError, ValueError):
            pass
        return sq.answer.strip().upper()

    def compute_fingerprint(q: Question, strategy: str = "strict") -> str:
        parts = []
        if q.stem:
            parts.append(_normalize_text(q.stem))
        if q.shared_options and strategy == "strict":
            parts.extend(sorted(_normalize_text(o) for o in q.shared_options))
        for sq in q.sub_questions:
            parts.append(_normalize_text(sq.text))
            if strategy == "strict":
                parts.extend(sorted(_normalize_text(o) for o in sq.options))
                parts.append(_resolve_answer_text(sq))
        return hashlib.sha256("|".join(parts).encode()).hexdigest()[:16]

    def save_bank(questions, output, password=None, compress=True, compress_level=6):
        fp = output.with_suffix(".mqb")
        fp.parent.mkdir(parents=True, exist_ok=True)
        payload = json.dumps(
            [asdict(q) for q in questions], ensure_ascii=False,
        ).encode("utf-8")
        if compress:
            payload = zlib.compress(payload, level=compress_level)
        salt = os.urandom(16)
        meta = {"count": len(questions), "created": time.time(),
                "encrypted": False, "compressed": compress, "salt_hex": salt.hex()}
        meta_bytes = json.dumps(meta, ensure_ascii=False).encode("utf-8")
        with open(fp, "wb") as fh:
            fh.write(b"MQB2")
            fh.write(len(meta_bytes).to_bytes(4, "big"))
            fh.write(meta_bytes)
            fh.write(payload)
        return fp


# ═══════════════════════════════════════════════════════════════
# 日志
# ═══════════════════════════════════════════════════════════════

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("scraper")


# ═══════════════════════════════════════════════════════════════
# 工具函数
# ═══════════════════════════════════════════════════════════════


def make_name() -> str:
    """生成时间戳名称，与现有爬虫产出格式一致: YYYY-MM-DD-HH-MM-SS-mmm"""
    now = datetime.now()
    return now.strftime("%Y-%m-%d-%H-%M-%S-") + f"{now.microsecond // 1000:03d}"


def strip_html(text: str) -> str:
    """去除 HTML 标签"""
    if not text:
        return ""
    text = re.sub(r'<br\s*/?>', '\n', text, flags=re.IGNORECASE)
    text = re.sub(r'</?p[^>]*>', '\n', text, flags=re.IGNORECASE)
    text = re.sub(r'<[^>]+>', '', text)
    text = (text.replace('&nbsp;', ' ').replace('&lt;', '<')
                .replace('&gt;', '>').replace('&amp;', '&')
                .replace('&#39;', "'").replace('&quot;', '"'))
    text = re.sub(r'\n{3,}', '\n\n', text)
    return text.strip()


# ── 图片 URL 辅助 ──
# 实测 API 返回的 image / analysis_image / topic_stem.image 可能为:
#   None                                   → 无图
#   ""                                     → 无图
#   "https://cdn.yhykwch.com/..."          → 完整 URL
#   "https://examapi_two.yhykwch.com/..."  → 完整 URL (多个 CDN 域名)
#   "/uploads/..."                         → 相对路径 (兜底补全)

_IMG_TAG_TEMPLATE = '<img src="{url}" style="max-width:100%;height:auto;" />'
_CDN_FALLBACK = 'https://cdn.yhykwch.com'


def _normalize_img_url(raw) -> str:
    """把 API 返回的原始值规整为可直接用的 HTTPS URL; 无效则返回空串"""
    if not raw or not isinstance(raw, str):
        return ""
    url = raw.strip()
    if not url:
        return ""
    if url.startswith(('http://', 'https://')):
        return url
    if url.startswith('//'):
        return 'https:' + url
    if url.startswith('/'):
        return _CDN_FALLBACK + url
    # 兜底: 当相对路径处理
    return _CDN_FALLBACK + '/' + url


def _append_image(text: str, raw_url) -> str:
    """在文本末尾追加 HTML <img> 标签; 若 raw_url 无效则返回原文本"""
    url = _normalize_img_url(raw_url)
    if not url:
        return text
    tag = _IMG_TAG_TEMPLATE.format(url=url)
    if not text:
        return tag
    # 用换行分隔, 便于前端 innerHTML 渲染时视觉分层
    return f"{text}\n{tag}"


def answer_texts_to_key(answer_texts: list, answer_option: list) -> str:
    """
    将 API 返回的答案文本列表映射回选项字母。

    answer_option: ["次氯酸钠", "过氧化氢溶液", "EDTA", "氯亚明", "氯仿"]
    answer:        ["EDTA"]
    → "C"
    """
    if not answer_texts or not answer_option:
        return ""
    keys = []
    for ans in answer_texts:
        ans = ans.strip()
        for idx, opt_text in enumerate(answer_option):
            if opt_text.strip() == ans:
                keys.append(chr(ord('A') + idx))
                break
    keys.sort()
    return "".join(keys)


def format_options(answer_option: list) -> list:
    """["次氯酸钠", "EDTA", ...] → ["A.次氯酸钠", "B.EDTA", ...]"""
    return [f"{chr(ord('A') + i)}.{text}" for i, text in enumerate(answer_option)]


def collect_leaves(node: dict, ancestors: list = None) -> list:
    """递归遍历题型树，收集叶子节点 → [(leaf, [ancestors...])]"""
    if ancestors is None:
        ancestors = []
    results = []
    children = node.get("children") or []
    if children:
        for child in children:
            results.extend(
                collect_leaves(child, ancestors + [node.get("name", "")])
            )
    elif node.get("topic_count", 0) > 0:
        results.append((node, ancestors))
    return results


def extract_api_data(resp: dict) -> list:
    """从 API 响应中提取题目列表，兼容多种嵌套格式"""
    data = resp.get("data")
    if isinstance(data, dict):
        inner = data.get("data", data)
        return inner if isinstance(inner, list) else []
    return data if isinstance(data, list) else []


# ═══════════════════════════════════════════════════════════════
# 题型 (mode) 识别
# ═══════════════════════════════════════════════════════════════


def detect_mode(leaf_name: str, ancestors: list) -> str:
    """
    根据分类树路径推断 mode。

    实际树:
      第一部分 单选题           → A1/A2型题
      第二部分 共用题干单选题    → A3/A4型题
      第三部分 案例分析题        → 案例分析题
    """
    full_path = " ".join(ancestors + [leaf_name])
    if "案例分析" in full_path:
        return "案例分析题"
    if "共用题干" in full_path or re.search(r'A3|A4', full_path, re.IGNORECASE):
        return "A3/A4型题"
    if re.search(r'B[1-9]?\s*型', full_path):
        return "B1型题"
    return "A1/A2型题"


# ═══════════════════════════════════════════════════════════════
# 转换逻辑
# ═══════════════════════════════════════════════════════════════


def _build_sub_question(aq: dict) -> SubQuestion:
    """把一条 API 题目转为 SubQuestion, 自动附加题干图 / 解析图"""
    answer_option = aq.get("answer_option") or []
    answer_texts = aq.get("answer") or []

    text = strip_html(aq.get("title", ""))
    text = _append_image(text, aq.get("image"))

    discuss = strip_html(aq.get("analysis", ""))
    discuss = _append_image(discuss, aq.get("analysis_image"))

    return SubQuestion(
        text=text,
        options=format_options(answer_option),
        answer=answer_texts_to_key(answer_texts, answer_option),
        discuss=discuss,
    )


def convert_questions(
    api_questions: list,
    unit: str,
    cls: str,
    mode: str,
) -> list:
    """
    将 API 返回的题目列表转为 Question 对象列表。

    利用 topic_stem_id 自动判断共用题干分组：
      - topic_stem_id 为 None → 独立题 (A1/A2)
      - 多道题共享同一 topic_stem_id → 共用题干 (A3/A4 或案例分析)

    每个 Question 自动:
      - 生成 name (时间戳)
      - 计算 fingerprint (sha256-based)
      - 在题干/解析末尾以 <img> 标签形式追加图片 URL
    """
    # 按 topic_stem_id 分组，保持原始顺序
    groups: OrderedDict = OrderedDict()
    for aq in api_questions:
        stem_id = aq.get("topic_stem_id")
        groups.setdefault(stem_id, []).append(aq)

    results = []

    for stem_id, group in groups.items():
        if stem_id is None:
            # ── 独立题 ──
            for aq in group:
                q = Question(
                    name=make_name(),
                    pkg=PKG_NAME,
                    cls=cls,
                    unit=unit,
                    mode=mode,
                    sub_questions=[_build_sub_question(aq)],
                    raw={},
                )
                q.fingerprint = compute_fingerprint(q, "strict")
                results.append(q)
        else:
            # ── 共用题干 ──
            stem_obj = group[0].get("topic_stem") or {}
            stem_text = strip_html(stem_obj.get("title", ""))
            stem_text = _append_image(stem_text, stem_obj.get("image"))

            actual_mode = mode
            if "案例分析" in stem_text:
                actual_mode = "案例分析题"

            q = Question(
                name=make_name(),
                pkg=PKG_NAME,
                cls=cls,
                unit=unit,
                mode=actual_mode,
                stem=stem_text,
                sub_questions=[],
                raw={},
            )

            for aq in group:
                q.sub_questions.append(_build_sub_question(aq))

            q.fingerprint = compute_fingerprint(q, "strict")
            results.append(q)

    return results


# ═══════════════════════════════════════════════════════════════
# 主逻辑
# ═══════════════════════════════════════════════════════════════


def main():
    print("=" * 60)
    print("  医海医考 → MQB 题库爬取工具")
    print("=" * 60)

    # ── 1. 登录 ──
    phone = input("\n手机号: ").strip()
    password = input("密码: ").strip()

    client = YhykClient()
    log.info("正在登录...")

    r = client.login(phone, password)
    if r.get("code") != 200:
        log.error("登录失败: %s", r.get("msg", "未知错误"))
        sys.exit(1)
    log.info("登录成功! Token: %s...", client.token[:20])
    # ── 2. 获取用户信息 ──
    user_info = client.get_user_info()
    speciality_name = ""
    exam_type_name = ""
    if user_info.get("code") == 200:
        data = user_info["data"]
        inner = data.get("data", data) if isinstance(data, dict) else data
        sp = inner.get("speciality", {}) if isinstance(inner, dict) else {}
        et = inner.get("exam_type", {}) if isinstance(inner, dict) else {}
        speciality_name = sp.get("name", "")
        exam_type_name = et.get("name", "")
        log.info("考试类型: %s", exam_type_name)
        log.info("专业: %s", speciality_name)

    # ── 3. 获取题型分类树 ──
    log.info("获取题型分类树...")
    tl = client.topic_type_list()
    if tl.get("code") != 200:
        log.error("获取题型列表失败: %s", tl.get("msg", "未知错误"))
        sys.exit(1)

    items = extract_api_data(tl)
    log.info("共 %d 个顶级分类", len(items))
    for i, top in enumerate(items, 1):
        log.info("  %d. %s (id=%s)", i, top.get("name", "?"), top.get("id", "?"))

    # ── 4. 收集所有叶子节点 ──
    all_leaves = []
    for top_item in items:
        all_leaves.extend(collect_leaves(top_item))

    total_q_est = sum(leaf.get("topic_count", 0) for leaf, _ in all_leaves)
    log.info("共 %d 个叶子节点，预估 %d 题", len(all_leaves), total_q_est)

    # ── 5. 逐个叶子节点爬取 ──
    all_questions: list = []
    failed_nodes = []
    request_count = 0
    img_stats = {'stem': 0, 'text': 0, 'discuss': 0}

    for idx, (leaf, ancestors) in enumerate(all_leaves, 1):
        leaf_id = leaf["id"]
        leaf_name = leaf.get("name", "")
        topic_count = leaf.get("topic_count", 0)

        # unit = 顶级分类名
        unit_name = ""
        for anc in ancestors:
            if anc and anc.strip():
                unit_name = anc.strip()
                break

        mode = detect_mode(leaf_name, ancestors)

        path_parts = [a for a in ancestors if a] + [leaf_name]
        path_str = " > ".join(path_parts)

        log.info("[%d/%d] %s (%d题, %s)",
                 idx, len(all_leaves), path_str, topic_count, mode)

        if request_count > 0:
            time.sleep(0.5)

        # 带重试的请求
        resp = None
        max_retries = 3
        for attempt in range(max_retries):
            try:
                resp = client.topic_type(leaf_id)
                request_count += 1
            except Exception as e:
                log.warning("  请求异常: %s", e)
                if attempt < max_retries - 1:
                    time.sleep(1)
                    continue
                failed_nodes.append((leaf_id, path_str, str(e)))
                break

            if resp.get("code") == 200:
                break
            elif resp.get("code") == 1011 and attempt < max_retries - 1:
                log.info("  签名错误，%.1fs 后重试 (%d/%d)...",
                         1 + attempt, attempt + 1, max_retries)
                time.sleep(1 + attempt)
            else:
                break

        if resp is None or resp.get("code") != 200:
            msg = resp.get("msg", "未知错误") if resp else "请求异常"
            log.warning("  API 错误 (code=%s): %s, 跳过",
                        resp.get("code") if resp else "?", msg)
            failed_nodes.append((leaf_id, path_str, msg))
            continue

        qs_data = extract_api_data(resp)

        if not qs_data:
            log.warning("  返回空列表, 跳过")
            continue

        qs_data.sort(key=lambda x: x.get("sort", 0))

        cls_name = speciality_name or exam_type_name or "医海医考"
        converted = convert_questions(qs_data, unit_name, cls_name, mode)
        all_questions.extend(converted)

        # 本批图片统计
        for q in converted:
            if '<img ' in (q.stem or ''):
                img_stats['stem'] += 1
            for sq in q.sub_questions:
                if '<img ' in (sq.text or ''):
                    img_stats['text'] += 1
                if '<img ' in (sq.discuss or ''):
                    img_stats['discuss'] += 1

        shared_count = sum(1 for q in converted if q.stem)
        total_sq = sum(len(q.sub_questions) for q in converted)
        detail = f"{len(qs_data)} 题 → {len(converted)} Question ({total_sq} SubQ)"
        if shared_count:
            detail += f", {shared_count} 组共用题干"
        log.info("  ✓ %s", detail)

    # ── 6. 统计 & 保存 ──
    print()
    log.info("=" * 50)
    log.info("爬取完成!")

    total_sq = sum(len(q.sub_questions) for q in all_questions)
    log.info("  Question 对象: %d", len(all_questions))
    log.info("  SubQuestion 总计: %d", total_sq)

    # 图片统计
    log.info("  图片嵌入统计 (含 <img> 标签的字段数):")
    log.info("    共用题干图: %d", img_stats['stem'])
    log.info("    题干图:     %d", img_stats['text'])
    log.info("    解析图:     %d", img_stats['discuss'])
    log.info("    合计:       %d", sum(img_stats.values()))

    if not all_questions:
        log.warning("没有获取到任何题目，退出")
        sys.exit(1)

    # 指纹去重检查
    fp_set = set()
    dup_count = 0
    for q in all_questions:
        if q.fingerprint in fp_set:
            dup_count += 1
        fp_set.add(q.fingerprint)
    if dup_count:
        log.info("  发现 %d 道重复题目 (指纹相同)", dup_count)
    else:
        log.info("  无重复题目")

    # 按 unit 统计
    unit_counts: dict = {}
    for q in all_questions:
        unit_counts[q.unit] = unit_counts.get(q.unit, 0) + len(q.sub_questions)
    log.info("  按章节:")
    for u, c in sorted(unit_counts.items()):
        log.info("    %s: %d 题", u, c)

    # 按 mode 统计
    mode_sq: dict = {}
    mode_q: dict = {}
    for q in all_questions:
        mode_sq[q.mode] = mode_sq.get(q.mode, 0) + len(q.sub_questions)
        mode_q[q.mode] = mode_q.get(q.mode, 0) + 1
    log.info("  按题型:")
    for m in sorted(mode_sq):
        log.info("    %s: %d 小题 (%d 组)", m, mode_sq[m], mode_q[m])

    for q in all_questions:
        q.raw = {}

    # ── 输出 ──
    short_name = speciality_name
    m = re.search(r'\d*([\u4e00-\u9fff]+)', speciality_name)
    if m:
        short_name = m.group(1)
        if len(short_name) > 8:
            short_name = short_name[:8]

    output_name = f"医海医考_{short_name}"
    output_path = Path(PROJECT_ROOT / "data" / "output" / output_name)

    log.info("保存题库: %s.mqb", output_path)
    saved_path = save_bank(all_questions, output_path)
    file_size = saved_path.stat().st_size

    log.info("✓ 保存成功!")
    log.info("  文件: %s", saved_path)
    log.info("  大小: %.1f KB", file_size / 1024)
    log.info("")
    log.info("使用方法:")
    if HAS_TOOLKIT:
        log.info("  med-exam quiz --bank %s", saved_path)
    else:
        log.info("  cd %s && pip install -e .", PROJECT_ROOT)
        log.info("  med-exam quiz --bank %s", saved_path)
    # JSON 备份
    json_path = saved_path.with_suffix(".json")
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump([asdict(q) for q in all_questions], f, ensure_ascii=False, indent=2)
    log.info("  JSON 备份: %s (%.1f KB)", json_path, json_path.stat().st_size / 1024)


if __name__ == "__main__":
    main()
