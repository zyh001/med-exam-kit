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
    if bank:
        click.echo("📂 从题库缓存加载...")
        questions = load_bank(Path(bank), password)
    else:
        click.echo("📂 加载题目...")
        questions = load_json_files(input_dir, parser_map)
        if not questions:
            click.echo("未找到任何题目，退出。")
            return
        if dedup:
            click.echo("🔍 去重中...")
            questions = deduplicate(questions, strategy)

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
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

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

    click.echo(f"题库加载完成: {len(questions)} 道题")

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
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    bank_path = Path(output).with_suffix(".mqb")
    existing = []

    # 已有文件且非 rebuild → 加载已有题目
    if bank_path.exists() and not rebuild:
        click.echo(f"📦 发现已有题库: {bank_path.name}")
        existing = load_bank(bank_path, password)
        click.echo(f"   已有 {len(existing)} 题")

    click.echo("📂 加载 JSON...")
    new_questions = load_json_files(input_dir, parser_map)
    if not new_questions and not existing:
        click.echo("未找到题目。")
        return

    if existing:
        click.echo(f"📥 发现 {len(new_questions)} 道待追加题目")
        combined = existing + new_questions
    else:
        combined = new_questions

    click.echo("🔍 去重中...")
    combined = deduplicate(combined, strategy)

    added = len(combined) - len(existing)

    fp = save_bank(combined, bank_path, password)

    click.echo(f"\n{'='*40}")
    if existing:
        click.echo(f"  原有: {len(existing)} 题")
        click.echo(f"  新增: {added} 题")
        click.echo(f"  重复跳过: {len(new_questions) - added} 题")
    click.echo(f"  总计: {len(combined)} 题")
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
    parser_map = cfg.get("parser_map", {
        "com.ahuxueshu": "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

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
@click.option("--provider", default="", help="AI provider: openai/deepseek/ollama")
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
def enrich(bank, input_dir, output, password, provider, model, api_key, base_url,
           max_workers, resume, checkpoint_dir, filter_modes, filter_units,
           limit, dry_run, only_missing, apply_ai, in_place, write_json):
    """AI 补全题库：为缺答案/缺解析的小题自动生成内容"""
    from med_exam_toolkit.ai.enricher import BankEnricher

    ctx_cfg    = click.get_current_context().obj.get("config", {})
    ai_cfg     = ctx_cfg.get("ai", {})
    parser_map = ctx_cfg.get("parser_map", {
        "com.ahuxueshu":      "ahuyikao",
        "com.yikaobang.yixue": "yikaobang",
    })

    # 参数优先级：命令行 > config.yaml > 默认值
    provider      = provider      or ai_cfg.get("provider",       "openai")
    model         = model         or ai_cfg.get("model",          "gpt-4o")
    api_key       = api_key       or ai_cfg.get("api_key",        "")
    base_url      = base_url      or ai_cfg.get("base_url",       "")
    max_workers   = max_workers   or int(ai_cfg.get("max_workers", 4))
    checkpoint_dir = checkpoint_dir or ai_cfg.get("checkpoint_dir", "data/checkpoints")

    if not bank and not input_dir:
        raise click.UsageError("必须指定 --bank 或 -i/--input-dir 中的一个")

    # 确定输出路径
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
    )
    enricher.run()

@cli.command()
@click.option("--bank", required=True, type=click.Path(exists=True), help=".mqb 题库路径")
@click.option("--password", default=None, help="题库密码")
@click.option("--mode", "filter_modes", multiple=True, help="过滤题型")
@click.option("--unit", "filter_units", multiple=True, help="过滤章节关键词")
@click.option("--keyword", default="", help="题干或题目关键词")
@click.option("--has-ai", is_flag=True, default=False, help="只显示含 AI 补全内容的题")
@click.option("--missing", is_flag=True, default=False, help="只显示缺答案或缺解析的题")
@click.option("--limit", default=20, type=int, help="最多显示多少小题（默认 20，0=全部）")
@click.option("--full", is_flag=True, default=False, help="显示完整解析（默认截断）")
def inspect(bank, password, filter_modes, filter_units, keyword, has_ai, missing, limit, full):
    """查看 .mqb 题库内容，支持过滤与搜索"""
    from med_exam_toolkit.bank import load_bank
    from collections import Counter
    import re as _re

    def _print_options(options: list[str]) -> list[str]:
        """渲染选项，避免双重字母前缀（原始数据已有 'A.' 前缀时不重复加）"""
        labels = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
        lines = []
        for oi, opt in enumerate(options or []):
            opt = opt.strip()
            if _re.match(r'^[A-Za-z][.．、]\s*', opt):  # 已有前缀
                lines.append(f"         {opt}")
            else:
                key = labels[oi] if oi < len(labels) else str(oi + 1)
                lines.append(f"         {key}. {opt}")
        return lines

    questions = load_bank(Path(bank), password)
    W = 70

    # ── 统计摘要 ──
    total_q  = len(questions)
    total_sq = sum(len(q.sub_questions) for q in questions)
    no_ans   = sum(1 for q in questions for sq in q.sub_questions
                   if not (sq.answer or "").strip())
    no_dis   = sum(1 for q in questions for sq in q.sub_questions
                   if not (sq.discuss or "").strip())
    has_ai_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.ai_answer or sq.ai_discuss)
    ai_ans_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.answer_source == "ai")
    ai_dis_c = sum(1 for q in questions for sq in q.sub_questions
                   if sq.discuss_source == "ai")
    mode_cnt = Counter(q.mode for q in questions)
    cls_cnt  = Counter(q.cls  for q in questions)

    click.echo(f"\n{'═' * W}")
    click.echo(f"  📦 题库：{bank}")
    click.echo(f"{'─' * W}")
    click.echo(f"  大题数：{total_q}    小题数：{total_sq}")
    click.echo(f"  缺答案：{no_ans}    缺解析：{no_dis}")
    click.echo(f"  含AI内容：{has_ai_c}    AI答案兜底：{ai_ans_c}    AI解析兜底：{ai_dis_c}")
    click.echo(f"\n  题型分布：")
    for m, c in sorted(mode_cnt.items()):
        click.echo(f"    {m or '未知':<14} {c:>5} 题")
    if len(cls_cnt) > 1:
        click.echo(f"\n  分类分布：")
        for c, n in sorted(cls_cnt.items(), key=lambda x: -x[1])[:10]:
            click.echo(f"    {c or '未知':<22} {n:>5} 题")
    click.echo(f"{'═' * W}")

    # ── 过滤 ──
    results: list[tuple[int, int]] = []
    for qi, q in enumerate(questions):
        if filter_modes and q.mode not in filter_modes:
            continue
        if filter_units and not any(kw in (q.unit or "") for kw in filter_units):
            continue
        if keyword and keyword not in (q.stem or "") and not any(
            keyword in sq.text for sq in q.sub_questions
        ):
            continue
        for si, sq in enumerate(q.sub_questions):
            if has_ai and not (sq.ai_answer or sq.ai_discuss):
                continue
            if missing and (sq.answer or "").strip() and (sq.discuss or "").strip():
                continue
            results.append((qi, si))

    has_filter = any([filter_modes, filter_units, keyword, has_ai, missing])
    if has_filter:
        click.echo(f"\n  🔎 过滤结果：{len(results)} 个小题\n")
    else:
        click.echo(f"\n  📋 题目列表（前 {limit if limit else '全部'} 个小题）\n")

    show = results if limit == 0 else results[:limit]

    for qi, si in show:
        q  = questions[qi]
        sq = q.sub_questions[si]

        # 答案/解析状态标注
        ans_flag = ("✅" if (sq.answer or "").strip()
                    else "🤖" if (sq.ai_answer or "").strip() else "❓")
        dis_flag = ("✅" if (sq.discuss or "").strip()
                    else "🤖" if (sq.ai_discuss or "").strip() else "❓")
        ai_tag   = " [AI]" if (sq.ai_answer or sq.ai_discuss) else ""

        click.echo(f"{'─' * W}")
        click.echo(
            f"  [{qi+1}-{si+1}]  {q.mode}  {q.unit or ''}  "
            f"答案:{ans_flag}  解析:{dis_flag}{ai_tag}"
        )

        # 题干（A3/A4 共享题干）
        if q.stem:
            stem_text = q.stem if full else _trunc(q.stem, 100)
            click.echo(f"  【题干】{stem_text}")

        # 题目
        text = sq.text or "(无题目文本)"
        text_disp = text if full else _trunc(text, 100)
        click.echo(f"  【题目】{text_disp}")

        # 选项（智能去重前缀）
        for line in _print_options(sq.options):
            click.echo(line)

        # 答案（优先正式，兜底 AI）
        eff_ans = sq.eff_answer
        if eff_ans:
            src = " (AI)" if sq.answer_source == "ai" else ""
            conf = f"  [置信:{sq.ai_confidence:.2f}  模型:{sq.ai_model}]" if sq.answer_source == "ai" else ""
            click.echo(f"  【答案】{eff_ans}{src}{conf}")

        # 解析（优先正式，兜底 AI）
        eff_dis = sq.eff_discuss
        if eff_dis:
            src = " (AI)" if sq.discuss_source == "ai" else ""
            dis_disp = eff_dis if full else _trunc(eff_dis, 150)
            click.echo(f"  【解析】{dis_disp}{src}")

    click.echo(f"{'─' * W}")
    if limit and len(results) > limit:
        click.echo(f"  … 还有 {len(results) - limit} 个，用 --limit 0 显示全部")
    click.echo()

def _trunc(s: str, n: int = 40) -> str:
    s = (s or "").replace("\n", " ").strip()
    return s[:n] + "…" if len(s) > n else s

def main():
    cli()


if __name__ == "__main__":
    main()
