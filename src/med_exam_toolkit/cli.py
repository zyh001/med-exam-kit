# src/med_exam_toolkit/cli.py
from __future__ import annotations
import click
import yaml
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate
from med_exam_toolkit.exporters import discover as discover_exporters, get_exporter


def _load_config(config_path: str) -> dict:
    p = Path(config_path)
    if p.exists():
        return yaml.safe_load(p.read_text(encoding="utf-8")) or {}
    return {}


@click.command()
@click.option("-c", "--config", "config_path", default="config.yaml", help="配置文件路径")
@click.option("-i", "--input-dir", default=None, help="输入目录（覆盖配置文件）")
@click.option("-o", "--output-dir", default=None, help="输出目录（覆盖配置文件）")
@click.option("-f", "--format", "formats", multiple=True, help="导出格式: csv/xlsx/docx/pdf/db")
@click.option("--dedup/--no-dedup", default=True, help="是否去重")
@click.option("--strategy", default=None, type=click.Choice(["content", "strict"]), help="去重策略")
@click.option("--db-url", default=None, help="数据库连接字符串")
def main(config_path, input_dir, output_dir, formats, dedup, strategy, db_url):
    """医学考试题目去重与多格式导出工具"""
    cfg = _load_config(config_path)

    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    output_dir = output_dir or cfg.get("output_dir", "./data/output")
    strategy = strategy or cfg.get("dedup_strategy", "strict")
    parser_map = cfg.get("parser_map", {
        "ahuyikao.com": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    # 导出格式：CLI 参数优先，否则读配置
    if not formats:
        export_cfg = cfg.get("export", {})
        formats = export_cfg.get("formats", ["xlsx"])

    if not db_url:
        db_cfg = cfg.get("export", {}).get("database", {})
        db_url = db_cfg.get("url")

    output_path = Path(output_dir)

    # 1. 加载
    click.echo("=" * 50)
    click.echo("📂 加载题目...")
    questions = load_json_files(input_dir, parser_map)
    if not questions:
        click.echo("未找到任何题目，退出。")
        return

    # 2. 去重
    if dedup:
        click.echo("🔍 去重中...")
        questions = deduplicate(questions, strategy)

    # 3. 导出
    discover_exporters()
    base_name = output_path / "questions"

    for fmt in formats:
        click.echo(f"📤 导出 {fmt.upper()}...")
        try:
            exporter = get_exporter(fmt)
            extra_kwargs = {}
            if fmt == "db" and db_url:
                extra_kwargs["db_url"] = db_url
            exporter.export(questions, base_name, **extra_kwargs)
        except KeyError as e:
            click.echo(f"[ERROR] {e}")
        except Exception as e:
            click.echo(f"[ERROR] 导出 {fmt} 失败: {e}")

    click.echo("=" * 50)
    click.echo(f"✅ 完成! 共 {len(questions)} 题")


if __name__ == "__main__":
    main()
