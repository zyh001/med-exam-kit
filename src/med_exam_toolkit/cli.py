from __future__ import annotations
import click
import yaml
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate
from med_exam_toolkit.stats import print_summary
from med_exam_toolkit.filters import FilterCriteria, apply_filters
from med_exam_toolkit.exporters import discover as discover_exporters, get_exporter


def _load_config(config_path: str) -> dict:
    p = Path(config_path)
    if p.exists():
        return yaml.safe_load(p.read_text(encoding="utf-8")) or {}
    return {}


@click.group()
@click.option("-c", "--config", "config_path", default="config.yaml", help="配置文件路径")
@click.pass_context
def cli(ctx, config_path):
    """医学考试题目去重与多格式导出工具"""
    ctx.ensure_object(dict)
    ctx.obj["config"] = _load_config(config_path)


@cli.command()
@click.option("-i", "--input-dir", default=None, help="输入目录")
@click.option("-o", "--output-dir", default=None, help="输出目录")
@click.option("-f", "--format", "formats", multiple=True, help="导出格式: csv/xlsx/docx/pdf/db")
@click.option("--dedup/--no-dedup", default=True, help="是否去重")
@click.option("--strategy", default=None, type=click.Choice(["content", "strict"]))
@click.option("--db-url", default=None, help="数据库连接字符串")
@click.option("--mode", "filter_modes", multiple=True, help="过滤题型，如 A1 B1")
@click.option("--unit", "filter_units", multiple=True, help="过滤章节关键词")
@click.option("--keyword", default="", help="题干关键词搜索")
@click.option("--min-rate", default=0, type=int, help="最低正确率")
@click.option("--max-rate", default=100, type=int, help="最高正确率")
@click.option("--stats/--no-stats", default=True, help="是否显示统计")
@click.pass_context
def export(ctx, input_dir, output_dir, formats, dedup, strategy,
           db_url, filter_modes, filter_units, keyword, min_rate, max_rate, stats):
    """加载、去重、过滤、导出题目"""
    cfg = ctx.obj["config"]

    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    output_dir = output_dir or cfg.get("output_dir", "./data/output")
    strategy = strategy or cfg.get("dedup_strategy", "strict")
    parser_map = cfg.get("parser_map", {
        "ahuyikao.com": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    if not formats:
        export_cfg = cfg.get("export", {})
        formats = export_cfg.get("formats", ["xlsx"])

    if not db_url:
        db_cfg = cfg.get("export", {}).get("database", {})
        db_url = db_cfg.get("url")

    output_path = Path(output_dir)

    # 1. 加载
    click.echo("📂 加载题目...")
    questions = load_json_files(input_dir, parser_map)
    if not questions:
        click.echo("未找到任何题目，退出。")
        return

    # 2. 去重
    if dedup:
        click.echo("🔍 去重中...")
        questions = deduplicate(questions, strategy)

    # 3. 过滤
    criteria = FilterCriteria(
        modes=list(filter_modes),
        units=list(filter_units),
        keyword=keyword,
        min_rate=min_rate,
        max_rate=max_rate,
    )
    has_filter = any([filter_modes, filter_units, keyword, min_rate > 0, max_rate < 100])
    if has_filter:
        click.echo("🔎 过滤中...")
        questions = apply_filters(questions, criteria)

    if not questions:
        click.echo("过滤后无题目，退出。")
        return

    # 4. 统计
    if stats:
        print_summary(questions)

    # 5. 导出
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

    click.echo(f"✅ 完成! 共 {len(questions)} 题")


@cli.command()
@click.option("-i", "--input-dir", default=None, help="输入目录")
@click.pass_context
def info(ctx, input_dir):
    """仅查看统计信息，不导出"""
    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", {
        "ahuyikao.com": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    questions = load_json_files(input_dir, parser_map)
    if questions:
        questions = deduplicate(questions, "strict")
        print_summary(questions)


def main():
    cli()


if __name__ == "__main__":
    main()
