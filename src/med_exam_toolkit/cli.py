from __future__ import annotations
import click
import yaml
import sys
import json as _json
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate, compute_fingerprint
from med_exam_toolkit.stats import print_summary
from med_exam_toolkit.bank import save_bank, load_bank
from med_exam_toolkit.filters import FilterCriteria, apply_filters
from med_exam_toolkit.exporters import discover as discover_exporters, get_exporter
from med_exam_toolkit.exam import ExamConfig, ExamGenerator, ExamGenerationError, ExamDocxExporter
from med_exam_toolkit.parsers import DEFAULT_PARSER_MAP

def _load_config(config_path: str) -> dict:
    p = Path(config_path)
    if p.exists():
        return yaml.safe_load(p.read_text(encoding="utf-8")) or {}
    return {}

def parse_per_mode(raw: str) -> dict[str, int]:
    """解析 per-mode 参数, 支持 JSON 和简写格式"""
    raw = raw.strip()

    if raw.startswith("{"):
        try:
            return {k: int(v) for k, v in _json.loads(raw).items()}
        except (_json.JSONDecodeError, ValueError):
            pass

    try:
        result = {}
        for pair in raw.split(","):
            pair = pair.strip()
            if not pair:
                continue
            key, val = pair.rsplit(":", 1)
            result[key.strip()] = int(val.strip())
        if result:
            return result
    except (ValueError, IndexError):
        pass

    raise click.BadParameter(
        f"无法解析: {raw}\n"
        f"支持格式:\n"
        f'  JSON:  \'{{"A1型题": 30, "A2型题": 20}}\'\n'
        f"  简写:  A1型题:30,A2型题:20"
    )

@click.group()
@click.version_option(package_name="med-exam-toolkit", prog_name="med-exam-kit")
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
@click.option("--bank", default=None, type=click.Path(exists=True), help="直接从 .mqb 题库加载")
@click.option("--password", default=None, help="题库解密密码")
@click.pass_context
def export(ctx, input_dir, output_dir, formats, split_options, dedup, strategy,
           db_url, filter_modes, filter_units, keyword, min_rate, max_rate, stats
           , bank, password):
    """加载、去重、过滤、导出题目"""
    cfg = ctx.obj["config"]

    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    output_dir = output_dir or cfg.get("output_dir", "./data/output")
    strategy = strategy or cfg.get("dedup_strategy", "strict")
    parser_map = cfg.get("parser_map", DEFAULT_PARSER_MAP)

    if not formats:
        export_cfg = cfg.get("export", {})
        formats = export_cfg.get("formats", ["xlsx"])

    if not db_url:
        db_cfg = cfg.get("export", {}).get("database", {})
        db_url = db_cfg.get("url")

    output_path = Path(output_dir)

    # 1. 加载
    if bank:
        click.echo("📂 从题库缓存加载...")
        questions = load_bank(Path(bank), password)
        click.echo(f"   加载完成: {len(questions)} 道大题, {sum(len(q.sub_questions) for q in questions)} 道小题")
    else:
        click.echo("📂 加载题目...")
        questions = load_json_files(input_dir, parser_map)
        if not questions:
            click.echo("未找到任何题目，退出。")
            return

        total_before = len(questions)
        subq_before = sum(len(q.sub_questions) for q in questions)
        click.echo(f"   加载完成: {total_before} 道大题, {subq_before} 道小题")

        # 2. 去重
        if dedup:
            click.echo("🔍 去重中...")
            questions = deduplicate(questions, strategy)
            total_after = len(questions)
            subq_after = sum(len(q.sub_questions) for q in questions)
            click.echo(f"   去重完成: {total_after} 道大题, {subq_after} 道小题 (去除 {total_before - total_after} 道重复大题)")

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
@click.option("--cls", multiple=True, help="限定题库分类 (可多选)")
@click.option("--unit", multiple=True, help="限定章节 (可多选)")
@click.option("--mode", multiple=True, help="限定题型 (可多选)")
@click.option("-n", "--count", default=50, type=int, help="总抽题数")
@click.option("--count-mode", type=click.Choice(["sub", "question"]), default="sub",
              help="计数模式: sub=按小题(默认), question=按大题")
@click.option("--per-mode", default="", help='按题型指定数量, 如 A1型题:30,A2型题:20')
@click.option("--difficulty", default="", help="按难度比例抽题, 如 easy:20,medium:40,hard:30,extreme:10")
@click.option("--difficulty-mode", type=click.Choice(["global", "per_mode"]), default="global",
              help="难度分配策略: global=先难度后题型(默认), per_mode=先题型后难度")
@click.option("--seed", default=None, type=int, help="随机种子 (固定种子可复现)")
@click.option("--show-answers/--hide-answers", default=False, help="题目中显示答案")
@click.option("--answer-sheet/--no-answer-sheet", default=True, help="末尾附答案页")
@click.option("--show-discuss/--no-discuss", default=False, help="答案页附解析")
@click.option("--total-score", default=100, type=int, help="总分，不指定则默认“100”分")
@click.option("--score", default=None, type=float, help="每题分值, 不指定则由总分自动计算, 0=不显示")
@click.option("--time-limit", default=120, type=int, help="考试时间(分钟)")
@click.option("--dedup/--no-dedup", default=True, help="是否去重")
@click.option("--bank", default=None, type=click.Path(exists=True), help="从 .mqb 题库加载")
@click.option("--password", default=None, help="题库解密密码")
@click.pass_context
def generate(ctx, input_dir, output, title, subtitle, cls, unit, mode, count, count_mode,
             per_mode, difficulty, difficulty_mode, seed, show_answers, answer_sheet,
             show_discuss, total_score, score, time_limit, dedup, bank, password):
    """自动组卷: 随机抽题 → 导出 Word 试卷"""

    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", DEFAULT_PARSER_MAP)

    # 加载题库
    if bank:
        questions = load_bank(Path(bank), password)
    else:
        questions = load_json_files(input_dir, parser_map)
        if not questions:
            click.echo("题库为空。")
            sys.exit(1)
        if dedup:
            questions = deduplicate(questions, "strict")

    total_subq = sum(len(q.sub_questions) for q in questions)
    click.echo(f"题库加载完成: {len(questions)} 道大题, {total_subq} 道小题")

    # 解析 per_mode
    mode_dist = {}
    if per_mode:
        mode_dist = parse_per_mode(per_mode)

    # 解析 difficulty
    diff_dist = {}
    if difficulty:
        diff_dist = parse_per_mode(difficulty)
        valid = {"easy", "medium", "hard", "extreme"}
        bad = set(diff_dist.keys()) - valid
        if bad:
            click.echo(f"[ERROR] 无效难度等级: {bad}，支持: easy / medium / hard / extreme")
            click.echo("  easy=简单(≥80%) medium=中等(60-80%) hard=较难(40-60%) extreme=困难(<40%)")
            sys.exit(1)

    # 组卷配置
    exam_cfg = ExamConfig(
        title=title,
        subtitle=subtitle,
        time_limit=time_limit,
        cls_list=list(cls),
        units=list(unit),
        modes=list(mode),
        count=count,
        per_mode=mode_dist or None,
        count_mode=count_mode,
        total_score=total_score,
        difficulty_dist=diff_dist or None,
        difficulty_mode = difficulty_mode,
        seed=seed,
        show_answers=show_answers,
        answer_sheet=answer_sheet,
        show_discuss=show_discuss,
        score_per_sub=score or None,
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
@click.option("-i", "--input-dir", default=None, help="JSON 文件目录")
@click.option("-o", "--output", default="./data/output/questions", help="输出路径 (.mqb)")
@click.option("--password", default=None, help="加密密码 (留空则不加密)")
@click.option("--strategy", default="strict", type=click.Choice(["content", "strict"]))
@click.option("--rebuild", is_flag=True, help="强制重建, 忽略已有题库")
@click.pass_context
def build(ctx, input_dir, output, password, strategy, rebuild):
    """构建题库缓存 (.mqb), 已有文件时自动追加去重"""
    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", DEFAULT_PARSER_MAP)

    bank_path = Path(output).with_suffix(".mqb")
    existing = []

    # 已有文件且非 rebuild → 加载已有题目
    if bank_path.exists() and not rebuild:
        click.echo(f"📦 发现已有题库: {bank_path.name}")
        existing = load_bank(bank_path, password)
        existing_subq = sum(len(q.sub_questions) for q in existing)
        click.echo(f"   已有 {len(existing)} 道大题, {existing_subq} 道小题")

    click.echo("📂 加载 JSON...")
    new_questions = load_json_files(input_dir, parser_map)
    if not new_questions and not existing:
        click.echo("未找到题目。")
        return

    new_subq = sum(len(q.sub_questions) for q in new_questions)
    if existing:
        click.echo(f"📥 发现 {len(new_questions)} 道待追加大题, {new_subq} 道小题")
        combined = existing + new_questions
    else:
        combined = new_questions

    click.echo("🔍 去重中...")
    combined = deduplicate(combined, strategy)

    added = len(combined) - len(existing)
    combined_subq = sum(len(q.sub_questions) for q in combined)

    fp = save_bank(combined, bank_path, password)

    click.echo(f"\n{'='*40}")
    if existing:
        existing_subq = sum(len(q.sub_questions) for q in existing)
        added_subq = combined_subq - existing_subq
        click.echo(f"  原有: {len(existing)} 道大题, {existing_subq} 道小题")
        click.echo(f"  新增: {added} 道大题, {added_subq} 道小题")
        click.echo(f"  重复跳过: {len(new_questions) - added} 道大题")
    click.echo(f"  总计: {len(combined)} 道大题, {combined_subq} 道小题")
    click.echo(f"  文件: {fp}")
    click.echo(f"{'='*40}")

    print_summary(combined, full=True)
    click.echo("✅ 题库构建完成")

@cli.command()
@click.option("-i", "--input-dir", default=None, help="输入目录")
@click.option("--bank", default=None, type=click.Path(exists=True), help="从 .mqb 题库加载")
@click.option("--password", default=None, help="题库密码")
@click.pass_context
def info(ctx, input_dir, bank, password):
    """仅查看统计信息，不导出"""
    cfg = ctx.obj["config"]
    input_dir = input_dir or cfg.get("input_dir", "./data/raw")
    parser_map = cfg.get("parser_map", DEFAULT_PARSER_MAP)

    if bank:
        questions = load_bank(Path(bank), password)
    else:
        questions = load_json_files(input_dir, parser_map)
        if questions:
            questions = deduplicate(questions, "strict")

    if questions:
        print_summary(questions, full=True)
    else:
        click.echo("题库为空。")

@cli.command()
@click.option("--bank", required=True, type=click.Path(exists=True),
              help="旧版 MQB1 题库路径（.mqb）")
@click.option("-o", "--output", default=None,
              help="输出路径（默认在原文件名后追加 _v2，如 questions_v2.mqb）")
@click.option("--password", default=None, help="旧版题库的加密密码")
@click.option("--new-password", default=None, help="新版题库的加密密码（留空则不加密）")
@click.option("--yes", "-y", is_flag=True, default=False,
              help="跳过确认提示，直接执行迁移")
def migrate(bank, output, password, new_password, yes):
    """将旧版 MQB1 (pickle) 题库迁移为安全的 MQB2 (JSON) 格式

    \b
    迁移是一次性操作：
      1. 使用 pickle 读取旧版文件（仅此步骤使用 pickle）
      2. 立即转存为无 pickle 的 MQB2 格式
      3. 迁移完成后，旧版文件可以安全删除

    \b
    示例：
      med-exam migrate --bank old.mqb
      med-exam migrate --bank old.mqb -o new.mqb
      med-exam migrate --bank old.mqb --password 旧密码 --new-password 新密码
    """
    from med_exam_toolkit.bank import load_bank_legacy, save_bank

    bank_path = Path(bank)

    # 确定输出路径：默认在文件名末尾加 _v2
    if output:
        output_path = Path(output).with_suffix(".mqb")
    else:
        output_path = bank_path.with_name(bank_path.stem + "_v2.mqb")

    # 先检测文件格式，确认确实是 MQB1，避免用户误操作
    with open(bank_path, "rb") as fh:
        magic = fh.read(4)

    if magic == b"MQB2":
        click.echo("ℹ️  该文件已经是新版 MQB2 格式，无需迁移。")
        return
    if magic != b"MQB1":
        click.echo(f"[ERROR] 不是有效的 .mqb 文件（magic={magic!r}），已中止。")
        raise SystemExit(1)

    # 打印醒目的安全警告，让用户知晓 pickle 的风险和适用前提
    click.echo()
    click.echo("  ⚠️  安全提示  ".center(60, "─"))
    click.echo("  本操作将使用 pickle 读取旧版 MQB1 文件。")
    click.echo("  pickle 在反序列化时可以执行任意代码，存在安全风险。")
    click.echo("  请确认以下两点后再继续：")
    click.echo("    1. 该文件是由你自己的 med-exam-kit 生成的")
    click.echo("    2. 文件在生成后未被他人修改过")
    click.echo("─" * 60)
    click.echo(f"  源文件：{bank_path}")
    click.echo(f"  输出至：{output_path}")
    if new_password:
        click.echo("  新文件将使用新密码加密")
    elif password:
        click.echo("  新文件将不加密（如需加密请指定 --new-password）")
    click.echo("─" * 60)
    click.echo()

    # 未传 --yes 时交互确认，给用户最后一次反悔的机会
    if not yes:
        confirmed = click.confirm("确认继续迁移？", default=False)
        if not confirmed:
            click.echo("已取消。")
            return

    # ── 读取旧版文件 ──
    click.echo("📂 读取旧版 MQB1 文件...")
    try:
        questions = load_bank_legacy(bank_path, password)
    except ValueError as e:
        click.echo(f"[ERROR] {e}")
        raise SystemExit(1)

    total_subq = sum(len(q.sub_questions) for q in questions)
    click.echo(f"   读取完成：{len(questions)} 道大题，{total_subq} 道小题")

    # ── 写入新版文件 ──
    click.echo("💾 写入新版 MQB2 文件...")
    try:
        fp = save_bank(questions, output_path.with_suffix(""), new_password)
    except Exception as e:
        click.echo(f"[ERROR] 写入失败：{e}")
        raise SystemExit(1)

    click.echo()
    click.echo("  ✅ 迁移完成  ".center(60, "─"))
    click.echo(f"  新文件：{fp}")
    click.echo(f"  格式：MQB2 (JSON，无 pickle)")
    if new_password:
        click.echo("  加密：已用新密码加密（随机盐）")
    else:
        click.echo("  加密：未加密")
    click.echo()
    click.echo("  旧版文件仍保留在原位，确认新文件正常后可手动删除：")
    click.echo(f"    rm {bank_path}")
    click.echo("─" * 60)

@cli.command(hidden=True)
@click.option("--bank", required=True, type=click.Path(exists=True))
@click.option("--password", default=None)
@click.pass_context
def reindex(ctx, bank, password):
    """重算题库内所有指纹"""
    path = Path(bank)
    questions = load_bank(path, password)
    for q in questions:
        q.fingerprint = compute_fingerprint(q, "strict")
    save_bank(questions, path, password)
    click.echo(f"[OK] 已重算 {len(questions)} 条指纹")

@cli.command()
@click.option("--bank", default=None, type=click.Path(exists=True), help="输入 .mqb 题库")
@click.option("-i", "--input-dir", default=None, help="JSON 文件目录（与 --bank 二选一）")
@click.option("-o", "--output", default=None, help="输出路径（.mqb），不填则自动命名 *_ai.mqb")
@click.option("--password", default=None, help="题库密码")
@click.option("--provider", default="", help="AI provider: openai/deepseek/qwen/ollama 等")
@click.option("--model", default="", help="模型名")
@click.option("--api-key", default="", envvar="OPENAI_API_KEY", help="API Key（也可用环境变量 OPENAI_API_KEY）")
@click.option("--base-url", default="", help="自定义 API Base URL")
@click.option("--max-workers", default=0, type=int, help="并发数（默认 4）")
@click.option("--resume/--no-resume", default=True, help="是否断点续跑")
@click.option("--checkpoint-dir", default="", help="断点目录")
@click.option("--mode", "filter_modes", multiple=True, help="仅处理指定题型，如 A1型题")
@click.option("--unit", "filter_units", multiple=True, help="仅处理包含关键词的章节")
@click.option("--limit", default=0, type=int, help="最多处理多少小题，0=不限制")
@click.option("--dry-run", is_flag=True, help="仅预览待处理列表，不实际调用 AI")
@click.option("--only-missing/--force", default=True,
              help="仅补缺失字段（默认）/ 强制重新生成所有")
@click.option("--apply-ai", is_flag=True, default=False,
              help="将 AI 结果写入 answer/discuss 正式字段（默认只写 ai_answer/ai_discuss）")
@click.option("--in-place", is_flag=True, default=False,
              help="--bank 模式下就地修改原文件（配合 --apply-ai 使用）")
@click.option("--write-json", is_flag=True, default=False,
              help="--input-dir 模式下将结果写回原始 JSON 文件（而不是生成 .mqb）")
@click.option("--timeout", default=0, type=float,
              help="AI 请求超时秒数（推理/思考模型建议 180+，0=自动推断）")
@click.option("--thinking/--no-thinking", default=None,
              help="混合思考模型（如 Qwen3）是否开启深度思考；纯推理模型（o1/R1）忽略此参数")
def enrich(bank, input_dir, output, password, provider, model, api_key, base_url,
           max_workers, resume, checkpoint_dir, filter_modes, filter_units,
           limit, dry_run, only_missing, apply_ai, in_place, write_json,
           timeout, thinking):
    """AI 补全题库：为缺答案/缺解析的小题自动生成内容

    \b
    数据来源（二选一）：
      --bank          从已有 .mqb 题库文件读取
      -i/--input-dir  从 JSON 原始文件目录读取（自动去重）

    \b
    输出规则：
      默认                    AI 结果存入 ai_answer/ai_discuss，另存为 *_ai.mqb
      --apply-ai              同时写入 answer/discuss 正式字段
      --apply-ai --in-place   就地覆盖原 .mqb 文件
      -i + --write-json       结果写回每个原始 JSON 文件（就地修改）

    \b
    模型示例：
      普通模型:  --model gpt-4o
      纯推理:    --model o3-mini / --model deepseek-reasoner
      混合思考:  --model qwen3-235b-a22b --thinking      (开启思考)
                --model qwen3-235b-a22b --no-thinking   (关闭思考，更快)
    """
    from med_exam_toolkit.ai.enricher import BankEnricher
    from med_exam_toolkit.ai.client import is_reasoning_model, is_hybrid_thinking_model

    ctx_cfg    = click.get_current_context().obj.get("config", {})
    ai_cfg     = ctx_cfg.get("ai", {})
    parser_map = ctx_cfg.get("parser_map", DEFAULT_PARSER_MAP)

    # 参数优先级：命令行 > config.yaml > 默认值
    provider       = provider       or ai_cfg.get("provider",       "openai")
    model          = model          or ai_cfg.get("model",          "gpt-4o")
    api_key        = api_key        or ai_cfg.get("api_key",        "")
    base_url       = base_url       or ai_cfg.get("base_url",       "")
    max_workers    = max_workers    or int(ai_cfg.get("max_workers", 4))
    checkpoint_dir = checkpoint_dir or ai_cfg.get("checkpoint_dir", "data/checkpoints")

    if not bank and not input_dir:
        raise click.UsageError("必须指定 --bank 或 -i/--input-dir 中的一个")

    # ── 模型类型检测 ──
    pure_r = is_reasoning_model(model)
    hybrid = is_hybrid_thinking_model(model)

    if pure_r:
        click.echo(f"  🧠 纯推理模型: {model}（始终开启深度思考）")
        if max_workers > 2:
            click.echo(f"  ⚠️  推理模型响应较慢，建议 --max-workers 1~2，当前: {max_workers}")
    elif hybrid:
        state = "开启 🧠" if thinking else "关闭（加 --thinking 可开启）"
        click.echo(f"  🔀 混合思考模型: {model}  深度思考: {state}")
        if thinking and max_workers > 2:
            click.echo(f"  ⚠️  思考模式响应较慢，建议 --max-workers 1~2，当前: {max_workers}")

    # ── 超时自动推断 ──
    use_slow         = pure_r or (hybrid and thinking)
    resolved_timeout = timeout or (180.0 if use_slow else 60.0)

    # ── 输出路径 ──
    resolved_in_place = in_place or (apply_ai and output is None and bank and not write_json)
    if resolved_in_place and bank:
        output_path = Path(bank)
        click.echo(f"  ⚠️  --apply-ai --in-place：将就地修改原文件 {output_path}")
    elif output:
        output_path = Path(output).with_suffix(".mqb")
    elif bank:
        output_path = Path(bank).with_name(Path(bank).stem + "_ai.mqb")
    elif write_json:
        output_path = None   # JSON 写回模式不需要 .mqb 输出
    else:
        out_dir = ctx_cfg.get("output_dir", "./data/output")
        output_path = Path(out_dir) / "questions_ai.mqb"

    enricher = BankEnricher(
        bank_path=Path(bank) if bank else None,
        output_path=output_path,
        input_dir=Path(input_dir) if input_dir else None,
        parser_map=parser_map,
        password=password,
        provider=provider,
        model=model,
        api_key=api_key,
        base_url=base_url,
        max_workers=max_workers,
        resume=resume,
        checkpoint_dir=Path(checkpoint_dir),
        modes_filter=list(filter_modes),
        chapters_filter=list(filter_units),
        limit=limit,
        dry_run=dry_run,
        only_missing=only_missing,
        apply_ai=apply_ai,
        in_place=resolved_in_place,
        write_json=write_json,
        timeout=resolved_timeout,
        enable_thinking=thinking,
    )
    enricher.run()

@cli.command()
@click.option("--bank", required=True, type=click.Path(exists=True), help=".mqb 题库路径")
@click.option("--password", default=None, help="题库密码")
@click.option("--mode", "filter_modes", multiple=True, help="过滤题型，如 A1型题")
@click.option("--unit", "filter_units", multiple=True, help="过滤章节关键词")
@click.option("--keyword", default="", help="题干或题目关键词")
@click.option("--has-ai", is_flag=True, default=False, help="只显示含 AI 补全内容的题")
@click.option("--missing", is_flag=True, default=False, help="只显示缺答案或缺解析的题")
@click.option("--limit", default=20, type=int, help="最多显示多少小题（默认 20，0=全部）")
@click.option("--full", is_flag=True, default=False, help="显示完整解析（默认截断至 150 字）")
@click.option("--show-ai", is_flag=True, default=False,
              help="同时显示 AI 原始输出（即使官方字段有值）")
def inspect(bank, password, filter_modes, filter_units, keyword,
            has_ai, missing, limit, full, show_ai):
    """查看 .mqb 题库内容，支持过滤与搜索

    \b
    示例：
      med-exam-kit inspect --bank questions.mqb
      med-exam-kit inspect --bank questions.mqb --missing
      med-exam-kit inspect --bank questions.mqb --has-ai --show-ai --full
      med-exam-kit inspect --bank questions.mqb --mode A1型题 --keyword 肝炎 --limit 5
    """
    from med_exam_toolkit.inspect import run_inspect
    run_inspect(bank, password, filter_modes, filter_units, keyword,
                has_ai, missing, limit, full, show_ai)

@cli.command()
@click.option("--bank", required=True, type=click.Path(exists=True), help=".mqb 题库路径")
@click.option("--password", default=None, help="题库密码")
@click.option("--port", default=5173, type=int, help="本地端口（默认 5173）")
@click.option("--host", default="127.0.0.1", help="监听地址（默认 127.0.0.1）")
@click.option("--no-browser", is_flag=True, default=False, help="不自动打开浏览器")
@click.option("--no-pin", is_flag=True, default=False, help="禁用访问码验证（仅限受信任的本地网络）")
def edit(bank, password, port, host, no_browser, no_pin):
    """在浏览器中编辑题库（本地 Web 编辑器）

    \b
    启动后在浏览器访问 http://127.0.0.1:5173
    支持：修改答案/解析、批量替换文本、删除题目
    按 Ctrl+C 退出，Ctrl+S 快速保存
    """
    from med_exam_toolkit.editor import start_editor
    start_editor(bank, port=port, host=host, no_browser=no_browser, password=password, no_pin=no_pin)

@cli.command()
@click.option("--bank", required=True, type=click.Path(exists=True), help=".mqb 题库路径")
@click.option("--password", default=None, help="题库密码")
@click.option("--port", default=5174, type=int, help="本地端口（默认 5174）")
@click.option("--host", default="127.0.0.1", help="监听地址（默认 127.0.0.1）")
@click.option("--no-browser", is_flag=True, default=False, help="不自动打开浏览器")
@click.option("--no-record", is_flag=True, default=False, help="不记录做题历史（不创建 .progress.db）")
def quiz(bank, password, port, host, no_browser, no_record):
    """启动医考练习 Web 应用（练习/考试/背题模式）

    \b
    启动后在浏览器访问 http://127.0.0.1:5174
    支持三种模式：练习模式、考试模式、背题模式
    按 Ctrl+C 退出
    """
    from med_exam_toolkit.quiz import start_quiz
    start_quiz(bank, port=port, host=host, no_browser=no_browser,
               password=password, no_record=no_record)


def main():
    cli()


if __name__ == "__main__":
    main()
