from __future__ import annotations
import json
import logging
import time
from pathlib import Path
from typing import Iterator
from med_exam_toolkit.models import Question
from med_exam_toolkit.parsers import get_parser, discover

logger = logging.getLogger(__name__)


def load_json_files(
    input_dir: str | Path,
    parser_map: dict[str, str],
    *,
    progress_interval: int = 100
) -> list[Question]:
    """
    扫描目录下所有 .json 文件，根据 pkg 字段分发到对应 parser。

    Args:
        input_dir: 输入目录路径
        parser_map: {"com.ahuxueshu": "ahuyikao", "com.yikaobang.yixue": "yikaobang"}
        progress_interval: 每处理多少个文件打印一次进度

    Returns:
        Question 对象列表
    """
    discover()  # 确保所有内置 parser 已注册

    input_path = Path(input_dir)
    if not input_path.exists():
        raise FileNotFoundError(f"输入目录不存在：{input_path}")

    questions: list[Question] = []
    skipped = 0
    processed = 0
    start_time = time.time()

    json_files = list(sorted(input_path.rglob("*.json")))
    total_files = len(json_files)
    logger.info("开始处理 %d 个文件...", total_files)

    for file_idx, fp in enumerate(json_files, 1):
        try:
            raw = json.loads(fp.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as e:
            logger.warning("跳过无法解析的文件 %s: %s", fp, e)
            skipped += 1
            continue

        pkg = raw.get("pkg", "")
        parser_name = parser_map.get(pkg)

        if parser_name is None:
            for key, name in parser_map.items():
                if key in pkg or pkg in key:
                    parser_name = name
                    break

        if parser_name is None:
            logger.warning("未知 pkg=%s，跳过文件 %s", pkg, fp.name)
            skipped += 1
            continue

        try:
            parser = get_parser(parser_name)
            q = parser.parse(raw)
            q.source_file = str(fp.resolve())
            processed += 1
            questions.append(q)

            if file_idx % progress_interval == 0 or file_idx == total_files:
                elapsed = time.time() - start_time
                rate = file_idx / elapsed if elapsed > 0 else 0
                logger.info(
                    "进度：%d/%d (%.1f%%)，处理 %d 题，跳过 %d 个，速度：%.1f 文件/秒",
                    file_idx, total_files, file_idx / total_files * 100,
                    processed, skipped, rate,
                )
        except Exception as e:
            logger.warning("解析失败 %s: %s", fp.name, e)
            skipped += 1

    elapsed = time.time() - start_time
    logger.info("加载完成：%d 题，跳过 %d 个文件，耗时 %.2f 秒", len(questions), skipped, elapsed)
    return questions


def load_json_files_streaming(
    input_dir: str | Path,
    parser_map: dict[str, str],
    *,
    progress_interval: int = 100
) -> Iterator[Question]:
    """
    流式加载 JSON 文件，逐个产生 Question 对象，适用于大型题库。
    """
    discover()

    input_path = Path(input_dir)
    if not input_path.exists():
        raise FileNotFoundError(f"输入目录不存在：{input_path}")

    skipped = 0
    processed = 0
    start_time = time.time()

    json_files = list(sorted(input_path.rglob("*.json")))
    total_files = len(json_files)
    logger.info(f"开始流式处理 {total_files} 个文件...")

    for file_idx, fp in enumerate(json_files, 1):
        try:
            raw = json.loads(fp.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as e:
            logger.warning(f"跳过无法解析的文件 {fp}: {e}")
            skipped += 1
            continue

        pkg = raw.get("pkg", "")
        parser_name = parser_map.get(pkg)

        if parser_name is None:
            for key, name in parser_map.items():
                if key in pkg or pkg in key:
                    parser_name = name
                    break

        if parser_name is None:
            logger.warning(f"未知 pkg={pkg}，跳过文件 {fp.name}")
            skipped += 1
            continue

        try:
            parser = get_parser(parser_name)
            q = parser.parse(raw)
            q.source_file = str(fp.resolve())
            processed += 1
            yield q

            if file_idx % progress_interval == 0 or file_idx == total_files:
                elapsed = time.time() - start_time
                rate = file_idx / elapsed if elapsed > 0 else 0
                logger.info(f"进度：{file_idx}/{total_files} ({file_idx/total_files*100:.1f}%), "
                            f"处理 {processed} 题，跳过 {skipped} 个，速度：{rate:.1f} 文件/秒")
        except Exception as e:
            logger.warning(f"解析失败 {fp.name}: {e}")
            skipped += 1

    elapsed = time.time() - start_time
    logger.info(f"流式加载完成：处理 {processed} 题，跳过 {skipped} 个文件，耗时 {elapsed:.2f} 秒")