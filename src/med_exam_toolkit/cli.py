from __future__ import annotations
import click
import yaml
import sys
import json as _json
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate
from med_exam_toolkit.stats import print_summary
from med_exam_toolkit.filters import FilterCriteria, apply_filters
from med_exam_toolkit.exporters import discover as discover_exporters, get_exporter
from med_exam_toolkit.exam import ExamConfig, ExamGenerator, ExamGenerationError, ExamDocxExporter

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
@click.option("--split-options/--merge-options", default=True, help="选项拆分为独立列 / 合并为单列")
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
def export(ctx, input_dir, output_dir, formats, split_options, dedup, strategy,
           db_url, filter_modes, filter_units, keyword, min_rate, max_rate, stats):
    """加载、去重、过滤、导出题目"""
    cfg = ctx.obj["config"]

    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    output_dir = output_dir or cfg.get("output_dir", "./data/output")
    strategy = strategy or cfg.get("dedup_strategy", "strict")
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
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
        print_summary(questions, full=False)

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
            exporter.export(questions, base_name, split_options=split_options, **extra_kwargs)
        except KeyError as e:
            click.echo(f"[ERROR] {e}")
        except Exception as e:
            click.echo(f"[ERROR] 导出 {fmt} 失败: {e}")

    click.echo(f"✅ 完成! 共 {len(questions)} 题")

@cli.command()
@click.option("-i", "--input-dir", default=None, help="JSON 文件目录")
@click.option("-o", "--output", default="./data/output/exam", help="输出路径")
@click.option("--title", default="模拟考试", help="试卷标题")
@click.option("--subtitle", default="", help="副标题")
@click.option("--unit", multiple=True, help="限定章节 (可多选)")
@click.option("--mode", multiple=True, help="限定题型 (可多选)")
@click.option("-n", "--count", default=50, type=int, help="总抽题数")
@click.option("--per-mode", default="", help='按题型指定数量, JSON格式: \'{"A1型题":20,"A2型题":15}\'')
@click.option("--seed", default=None, type=int, help="随机种子 (固定种子可复现)")
@click.option("--show-answers/--hide-answers", default=False, help="题目中显示答案")
@click.option("--answer-sheet/--no-answer-sheet", default=True, help="末尾附答案页")
@click.option("--show-discuss/--no-discuss", default=False, help="答案页附解析")
@click.option("--score", default=2.0, type=float, help="每题分值, 0=不显示")
@click.option("--time-limit", default=120, type=int, help="考试时间(分钟)")
@click.option("--dedup/--no-dedup", default=True, help="是否去重")
@click.pass_context
def generate(ctx, input_dir, output, title, subtitle, unit, mode, count,
             per_mode, seed, show_answers, answer_sheet, show_discuss,
             score, time_limit, dedup):
    """自动组卷: 随机抽题 → 导出 Word 试卷"""

    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    # 加载题库
    questions = load_json_files(input_dir, parser_map)
    if not questions:
        click.echo("题库为空，请检查输入目录。")
        sys.exit(1)

    if dedup:
        questions = deduplicate(questions, "strict")

    click.echo(f"题库加载完成: {len(questions)} 道题")

    # 解析 per_mode
    per_mode_dict = {}
    if per_mode:
        try:
            per_mode_dict = _json.loads(per_mode)
        except _json.JSONDecodeError:
            click.echo(f"[ERROR] --per-mode 格式错误，需要 JSON: {per_mode}")
            sys.exit(1)

    # 组卷配置
    exam_cfg = ExamConfig(
        title=title,
        subtitle=subtitle,
        time_limit=time_limit,
        units=list(unit),
        modes=list(mode),
        count=count,
        per_mode=per_mode_dict,
        seed=seed,
        show_answers=show_answers,
        answer_sheet=answer_sheet,
        show_discuss=show_discuss,
        score_per_question=score,
    )

    # 生成
    try:
        gen = ExamGenerator(questions, exam_cfg)
        selected = gen.generate()
        click.echo(gen.summary(selected))
    except ExamGenerationError as e:
        click.echo(f"[ERROR] {e}")
        sys.exit(1)

    # 导出
    exporter = ExamDocxExporter(exam_cfg)
    fp = exporter.export(selected, Path(output))
    click.echo(f"✅ 试卷已生成: {fp}")

@cli.command()
@click.option("-i", "--input-dir", default=None, help="输入目录")
@click.pass_context
def info(ctx, input_dir):
    """仅查看统计信息，不导出"""
    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    questions = load_json_files(input_dir, parser_map)
    if questions:
        questions = deduplicate(questions, "strict")
        print_summary(questions, full=True)


def main():
    cli()


if __name__ == "__main__":
    main()
