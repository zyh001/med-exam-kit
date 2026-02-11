from __future__ import annotations
import json
from pathlib import Path
from med_exam_toolkit.models import Question
from med_exam_toolkit.parsers import get_parser, discover


def load_json_files(input_dir: str | Path, parser_map: dict[str, str]) -> list[Question]:
    """
    扫描目录下所有 .json 文件，根据 pkg 字段分发到对应 parser。

    parser_map: {"ahuyikao.com": "ahuyikao", "com.yikaobang.yixue": "yikaobang"}
    """
    discover()  # 确保所有内置 parser 已注册

    input_path = Path(input_dir)
    if not input_path.exists():
        raise FileNotFoundError(f"输入目录不存在: {input_path}")

    questions: list[Question] = []
    skipped = 0

    for fp in sorted(input_path.rglob("*.json")):
        try:
            raw = json.loads(fp.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as e:
            print(f"[WARN] 跳过无法解析的文件 {fp}: {e}")
            skipped += 1
            continue

        pkg = raw.get("pkg", "")
        parser_name = parser_map.get(pkg)

        if parser_name is None:
            # 尝试模糊匹配
            for key, name in parser_map.items():
                if key in pkg or pkg in key:
                    parser_name = name
                    break

        if parser_name is None:
            print(f"[WARN] 未知 pkg={pkg}，跳过文件 {fp.name}")
            skipped += 1
            continue

        try:
            parser = get_parser(parser_name)
            q = parser.parse(raw)
            questions.append(q)
        except Exception as e:
            print(f"[WARN] 解析失败 {fp.name}: {e}")
            skipped += 1

    print(f"[INFO] 加载完成: {len(questions)} 题, 跳过 {skipped} 个文件")
    return questions
