"""BankEnricher — 批量 AI 补全小题答案/解析"""
from __future__ import annotations

import hashlib
import json
import logging
import threading
import time
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any

from med_exam_toolkit.ai.checkpoint import Checkpoint
from med_exam_toolkit.ai.client import make_client
from med_exam_toolkit.ai.prompt import build_subquestion_prompt
from med_exam_toolkit.ai.result import apply_to_subquestion, parse_response, validate_result
from med_exam_toolkit.bank import load_bank, save_bank
from med_exam_toolkit.models import Question

logger = logging.getLogger(__name__)

_W = 60  # 分隔线宽度


import unicodedata


def _trunc(s: str, n: int = 40) -> str:
    s = (s or "").replace("\n", " ").strip()
    return s[:n] + "…" if len(s) > n else s


def _cjk_len(s: str) -> int:
    """返回字符串的显示宽度（CJK 字符算 2）"""
    return sum(
        2 if unicodedata.east_asian_width(c) in ("W", "F") else 1
        for c in s
    )


def _ljust(s: str, width: int) -> str:
    """CJK 感知的左对齐填充"""
    pad = max(0, width - _cjk_len(s))
    return s + " " * pad


def _bar(done: int, total: int, bar_width: int = 20) -> str:
    """进度条，计数部分固定宽度避免抖动"""
    filled   = int(bar_width * done / total) if total else 0
    bar      = f"[{'█' * filled}{'░' * (bar_width - filled)}]"
    digits   = len(str(total))
    counter  = f"{done:>{digits}}/{total}"
    return f"{bar} {counter}"


class BankEnricher:
    _CKPT_TAG = "ai_enrich"

    def __init__(
        self,
        bank_path: Path | None = None,
        output_path: Path | None = None,
        input_dir: Path | None = None,
        parser_map: dict | None = None,
        password: str | None = None,
        provider: str = "openai",
        model: str = "gpt-4o",
        api_key: str = "",
        base_url: str = "",
        max_workers: int = 4,
        resume: bool = True,
        checkpoint_dir: Path = Path("data/checkpoints"),
        modes_filter: list[str] | None = None,
        chapters_filter: list[str] | None = None,
        limit: int = 0,
        dry_run: bool = False,
        only_missing: bool = True,
        apply_ai: bool = False,
        in_place: bool = False,
        write_json: bool = False,
        timeout: float = 60.0,
        enable_thinking: bool | None = None,
    ) -> None:
        if bank_path is None and input_dir is None:
            raise ValueError("bank_path 和 input_dir 至少指定一个")

        self.bank_path = Path(bank_path) if bank_path else None
        self.input_dir = Path(input_dir) if input_dir else None
        self.parser_map = parser_map or {}
        self.password = password

        self.provider = provider
        self.model = model
        self.api_key = api_key
        self.base_url = base_url
        self.max_workers = max_workers
        self.resume = resume
        self.modes_filter = modes_filter or []
        self.chapters_filter = chapters_filter or []
        self.limit = limit
        self.dry_run = dry_run
        self.only_missing = only_missing
        self.apply_ai = apply_ai
        self.in_place = in_place
        self.write_json = write_json
        self.timeout         = timeout
        self.enable_thinking = enable_thinking

        # 输出路径（.mqb），JSON 写回模式不需要
        if output_path:
            self.output_path = Path(output_path)
        elif apply_ai and in_place and bank_path:
            self.output_path = Path(bank_path)
        elif bank_path:
            stem = Path(bank_path).stem
            self.output_path = Path(bank_path).with_name(stem + "_ai.mqb")
        else:
            self.output_path = None

        self._shutdown = threading.Event()
        self._ckpt = Checkpoint(
            tag=self._CKPT_TAG,
            checkpoint_dir=Path(checkpoint_dir),
            id_fn=lambda t: t["task_id"],
        )
        self._questions: list[Question] = []
        self._start_time: float = 0.0

    # ─────────────────────────────── 入口 ───────────────────────────────

    def run(self) -> None:
        self._start_time = time.time()
        questions = self._load_questions()
        self._questions = questions

        if self.resume:
            loaded = self._ckpt.load()
            if loaded:
                print(f"  ♻️  断点恢复：已加载 {loaded} 条缓存记录")

        tasks = self._build_tasks(questions)
        if self.limit > 0:
            tasks = tasks[: self.limit]

        pending = list(self._ckpt.iter(tasks))
        already = len(tasks) - len(pending)

        self._print_plan(questions, tasks, already, pending)

        if self.dry_run:
            self._dry_run_preview(pending, questions)
            return

        if pending:
            self._process(pending, questions)
        else:
            print("\n  ✅ 无需处理，全部任务已完成")

        filled = self._apply_results(questions, tasks)
        self._write_output(questions, filled)

        elapsed = time.time() - self._start_time
        failed  = sum(1 for v in self._ckpt.results.values() if v is None)
        print(f"  ⏱  总耗时: {elapsed:.1f}s")
        if failed == 0:
            self._ckpt.clear()
        else:
            print(f"  ⚠️  {failed} 个小题调用失败，断点已保留，下次 --resume 可续跑")

    # ─────────────────────────────── 加载 ───────────────────────────────

    def _load_questions(self) -> list[Question]:
        print(f"\n{'═' * _W}")
        print(f"  📂 加载题库")
        print(f"{'─' * _W}")

        if self.bank_path and self.bank_path.exists():
            questions = load_bank(self.bank_path, self.password)
            print(f"  来源文件：{self.bank_path}")
            print(f"  格式：    .mqb 题库")
        elif self.input_dir:
            from med_exam_toolkit.loader import load_json_files
            from med_exam_toolkit.dedup import deduplicate
            questions = load_json_files(str(self.input_dir), self.parser_map)
            questions = deduplicate(questions, "strict")
            print(f"  来源目录：{self.input_dir}")
            print(f"  格式：    JSON 文件")
        else:
            raise FileNotFoundError(f"题库文件不存在: {self.bank_path}")

        mode_cnt = Counter(q.mode for q in questions)
        total_sq = sum(len(q.sub_questions) for q in questions)
        print(f"  题目数量：{len(questions)}  小题总数：{total_sq}")
        print(f"  题型分布：", end="")
        print("  ".join(f"{m or '未知'}×{c}" for m, c in sorted(mode_cnt.items())))
        print(f"{'─' * _W}")
        return questions

    # ─────────────────────────────── 写出 ───────────────────────────────

    def _write_output(self, questions: list[Question], filled: int) -> None:
        print(f"\n{'─' * _W}")
        print(f"  💾 写出结果")

        if self.write_json and self.input_dir:
            count = _write_back_json(questions)
            print(f"  ✅ 已就地回写 {count} 个 JSON 文件")
        elif self.output_path:
            self.output_path.parent.mkdir(parents=True, exist_ok=True)
            save_bank(questions, self.output_path, self.password)
            note = "（就地修改）" if self.output_path == self.bank_path else ""
            print(f"  ✅ 回填 {filled} 个小题 → {self.output_path} {note}")
        else:
            print("  ⚠️  未指定输出路径，结果未保存")

    # ─────────────────────────────── Task ───────────────────────────────

    def _build_tasks(self, questions: list[Question]) -> list[dict[str, Any]]:
        tasks: list[dict[str, Any]] = []
        for qi, q in enumerate(questions):
            if self.modes_filter and q.mode not in self.modes_filter:
                continue
            if self.chapters_filter and not any(
                kw in (q.unit or "") for kw in self.chapters_filter
            ):
                continue
            for si, sq in enumerate(q.sub_questions):
                has_answer  = bool((sq.answer  or "").strip())
                has_discuss = bool((sq.discuss or "").strip())
                if self.only_missing:
                    need_answer  = not has_answer
                    need_discuss = not has_discuss
                    if not (need_answer or need_discuss):
                        continue
                else:
                    need_answer = need_discuss = True

                tasks.append({
                    "task_id":     self._task_id(q, qi, si),
                    "qi":          qi,
                    "si":          si,
                    "need_answer":  need_answer,
                    "need_discuss": need_discuss,
                })
        return tasks

    @staticmethod
    def _task_id(q: Question, qi: int, si: int) -> str:
        base = f"{q.fingerprint}|{qi}|{si}|{(q.stem or '')[:80]}"
        return hashlib.md5(base.encode("utf-8")).hexdigest()

    # ─────────────────────────────── 输出 ───────────────────────────────

    def _print_plan(
        self,
        questions: list[Question],
        tasks: list[dict],
        already: int,
        pending: list[dict],
    ) -> None:
        total_sq = sum(len(q.sub_questions) for q in questions)
        no_ans   = sum(1 for q in questions for sq in q.sub_questions
                       if not (sq.answer  or "").strip())
        no_dis   = sum(1 for q in questions for sq in q.sub_questions
                       if not (sq.discuss or "").strip())
        need_ans = sum(1 for t in pending if t["need_answer"])
        need_dis = sum(1 for t in pending if t["need_discuss"])
        both     = sum(1 for t in pending if t["need_answer"] and t["need_discuss"])

        print(f"\n  📊 题库分析")
        print(f"{'─' * _W}")
        print(f"  小题总数:       {total_sq:>6}")
        print(f"  缺答案小题:     {no_ans:>6}")
        print(f"  缺解析小题:     {no_dis:>6}")
        print(f"\n  📋 本次任务")
        print(f"{'─' * _W}")
        print(f"  待增强小题:     {len(tasks):>6}")
        print(f"  已完成(缓存):   {already:>6}")
        print(f"  待处理:         {len(pending):>6}")
        if pending:
            print(f"    ├ 补答案:     {need_ans:>6}")
            print(f"    ├ 补解析:     {need_dis:>6}")
            print(f"    └ 答案+解析:  {both:>6}")
        print(f"\n  ⚙️  AI 配置")
        print(f"{'─' * _W}")
        print(f"  provider:  {self.provider}")
        print(f"  model:     {self.model}")
        print(f"  workers:   {self.max_workers}")
        from med_exam_toolkit.ai.client import is_reasoning_model, is_hybrid_thinking_model
        pure_r  = is_reasoning_model(self.model)
        hybrid  = is_hybrid_thinking_model(self.model)
        thinking_on = pure_r or (hybrid and self.enable_thinking)
        print(f"  apply-ai:  {'是（写回 answer/discuss 正式字段）' if self.apply_ai else '否（仅写 ai_answer/ai_discuss）'}")
        if pure_r:
            print(f"  推理模式:  是（纯推理模型，始终开启）")
        elif hybrid:
            state = "开启" if self.enable_thinking else "关闭（加 --thinking 开启）"
            print(f"  推理模式:  混合思考模型，当前思考：{state}")
        else:
            print(f"  推理模式:  否（普通模型）")
        print(f"  超时设置:  {self.timeout:.0f}s")
        if self.write_json:
            print(f"  输出模式:  就地修改原始 JSON 文件")
        elif self.output_path:
            in_p = self.output_path == self.bank_path
            print(f"  输出路径:  {self.output_path}{'（就地修改）' if in_p else ''}")
        print(f"{'═' * _W}\n")

    def _dry_run_preview(self, pending: list[dict], questions: list[Question]) -> None:
        print(f"  [DRY-RUN] 将处理 {len(pending)} 个小题（不实际调用 AI）\n")
        print(f"  {'#':>4}  {'题型':<8}  {'章节':<14}  {'任务':<10}  小题内容")
        print(f"  {'─'*4}  {'─'*8}  {'─'*14}  {'─'*10}  {'─'*35}")
        for i, t in enumerate(pending[:30], 1):
            q  = questions[t["qi"]]
            sq = q.sub_questions[t["si"]]
            job = ("答案+解析" if t["need_answer"] and t["need_discuss"]
                   else "补答案" if t["need_answer"] else "补解析")
            text = _trunc(sq.text or q.stem, 35)
            print(f"  {i:>4}  {q.mode:<8}  {_trunc(q.unit,14):<14}  {job:<10}  {text}")
        if len(pending) > 30:
            print(f"\n  … 还有 {len(pending) - 30} 个（仅展示前 30）")

    # ─────────────────────────────── 并发 ───────────────────────────────

    def _process(self, pending: list[dict], questions: list[Question]) -> None:
        client = make_client(
            provider=self.provider,
            api_key=self.api_key,
            base_url=self.base_url,
            model=self.model,
            timeout=self.timeout,
        )

        total   = len(pending)
        success = 0
        failed  = 0
        interrupted = False

        # 进度条宽 = [20] + 空格 + "digits/digits"
        _cnt_w = len(str(total)) * 2 + 1   # "nn/nn"
        C_BAR  = 22 + _cnt_w
        C_MODE, C_JOB, C_UNIT = 10, 10, 16

        print(f"  🚀 开始处理  按 Ctrl+C 可安全中断\n")
        print(
            f"  {_ljust('进度条', C_BAR)}  状态  "
            f"{_ljust('题型', C_MODE)}  {_ljust('任务', C_JOB)}  "
            f"{_ljust('章节', C_UNIT)}  AI结果预览"
        )
        print(f"  {'─'*C_BAR}  {'─'*4}  {'─'*C_MODE}  {'─'*C_JOB}  {'─'*C_UNIT}  {'─'*30}")

        with ThreadPoolExecutor(
            max_workers=self.max_workers,
            thread_name_prefix="ai-enrich",
        ) as pool:
            futures = {
                pool.submit(self._call_ai, client, t, questions): t
                for t in pending
            }
            try:
                for future in as_completed(futures):
                    if self._shutdown.is_set():
                        break

                    t    = futures[future]
                    done = success + failed + 1
                    q    = questions[t["qi"]]
                    sq   = q.sub_questions[t["si"]]
                    job  = ("答案+解析" if t["need_answer"] and t["need_discuss"]
                            else "补答案" if t["need_answer"] else "补解析")
                    bar  = _bar(done, total)

                    try:
                        result = future.result()
                        if result:
                            self._ckpt.done(t["task_id"], result)
                            success += 1
                            preview = ""
                            if result.get("answer"):
                                preview += f"答案={result['answer']}  "
                            if result.get("discuss"):
                                preview += _trunc(result["discuss"], 25)
                            conf = result.get("confidence", 0.0)
                            print(
                                f"  {_ljust(bar, C_BAR)}  ✅    "
                                f"{_ljust(q.mode or '', C_MODE)}  "
                                f"{_ljust(job, C_JOB)}  "
                                f"{_ljust(_trunc(q.unit or '', 8), C_UNIT)}  "
                                f"{preview} [置信:{conf:.2f}]"
                            )
                        else:
                            self._ckpt.done(t["task_id"], None)
                            failed += 1
                            print(
                                f"  {_ljust(bar, C_BAR)}  ❌    "
                                f"{_ljust(q.mode or '', C_MODE)}  "
                                f"{_ljust(job, C_JOB)}  "
                                f"{_ljust(_trunc(q.unit or '', 8), C_UNIT)}  "
                                f"AI 返回为空"
                            )
                    except Exception as exc:  # noqa: BLE001
                        self._ckpt.done(t["task_id"], None)
                        failed += 1
                        logger.exception("任务异常: %s", exc)
                        print(
                            f"  {_ljust(bar, C_BAR)}  ❌    "
                            f"{_ljust(q.mode or '', C_MODE)}  "
                            f"{_ljust(job, C_JOB)}  "
                            f"{_ljust(_trunc(q.unit or '', 8), C_UNIT)}  "
                            f"异常: {str(exc)[:35]}"
                        )

            except KeyboardInterrupt:
                interrupted = True
                self._shutdown.set()
                print("\n\n  ⚠️  Ctrl+C 中断，正在取消队列任务...")
                for f in futures:
                    f.cancel()
                pool.shutdown(wait=False, cancel_futures=True)

        elapsed = time.time() - self._start_time
        print(f"\n{'─' * _W}")
        if interrupted:
            untouched = total - success - failed
            print(
                f"  🛑 已中断  ✅成功: {success}  ❌失败: {failed}  "
                f"⏭跳过: {untouched}  ⏱耗时: {elapsed:.1f}s"
            )
            print(f"  断点已保存，下次 --resume 可续跑")
        else:
            avg = elapsed / total if total else 0
            print(
                f"  🏁 处理完成  ✅成功: {success}  ❌失败: {failed}  "
                f"共: {total}  ⏱耗时: {elapsed:.1f}s  均速: {avg:.1f}s/题"
            )

    # ─────────────────────────────── 单次 AI ───────────────────────────────

    def _call_ai(
        self,
        client: Any,
        task: dict[str, Any],
        questions: list[Question],
    ) -> dict[str, Any] | None:
        if self._shutdown.is_set():
            return None

        from med_exam_toolkit.ai.client import (
            build_chat_params, adapt_messages_for_reasoning,
            is_reasoning_model, is_hybrid_thinking_model,
            extract_response_text,
        )

        q  = questions[task["qi"]]
        sq = q.sub_questions[task["si"]]

        prompt = build_subquestion_prompt(
            q, sq,
            need_answer=task["need_answer"],
            need_discuss=task["need_discuss"],
        )

        # token 预算：开启思考时需要更多空间（思维链消耗）
        pure_r    = is_reasoning_model(self.model)
        hybrid    = is_hybrid_thinking_model(self.model)
        thinking  = self.enable_thinking   # 用户设置的开关
        use_large = pure_r or (hybrid and thinking)
        max_tokens = 8000 if use_large else 800

        raw_messages = [
            {"role": "system", "content": "你是医学考试辅导专家，严格按 JSON 输出，不要 markdown。"},
            {"role": "user",   "content": prompt},
        ]
        messages = adapt_messages_for_reasoning(self.model, raw_messages)
        params   = build_chat_params(
            model           = self.model,
            messages        = messages,
            temperature     = 0.2,
            max_tokens      = max_tokens,
            enable_thinking = thinking,
        )

        # 带指数退避的重试：应对速率限制（429）和瞬时网络抖动
        # 最多重试 3 次，等待间隔依次为 2s、4s、8s
        MAX_RETRIES  = 3
        base_delay   = 2.0
        last_exc: Exception | None = None

        for attempt in range(MAX_RETRIES + 1):
            if self._shutdown.is_set():
                return None

            try:
                response = client.chat.completions.create(**params)
                break   # 请求成功，跳出重试循环

            except Exception as exc:  # noqa: BLE001
                last_exc = exc
                if self._shutdown.is_set():
                    return None

                # 判断是否值得重试（速率限制或临时网络错误）
                exc_str = str(exc).lower()
                retryable = (
                    "429" in exc_str           # rate limit
                    or "rate limit" in exc_str
                    or "timeout" in exc_str
                    or "connection" in exc_str
                    or "502" in exc_str
                    or "503" in exc_str
                )

                if retryable and attempt < MAX_RETRIES:
                    delay = base_delay * (2 ** attempt)   # 2s → 4s → 8s
                    logger.warning(
                        "AI 请求失败 (attempt %d/%d)，%.0fs 后重试: %s",
                        attempt + 1, MAX_RETRIES, delay, exc,
                    )
                    time.sleep(delay)
                    continue

                # 不可重试，或已达最大次数
                logger.warning("AI 请求异常: %s", exc)
                return None
        else:
            # for-else：重试次数耗尽仍未成功
            logger.warning("AI 请求达到最大重试次数，放弃: %s", last_exc)
            return None

        content, reasoning_text = extract_response_text(response)

        if reasoning_text:
            logger.debug("推理过程 (%d chars): %s…", len(reasoning_text), reasoning_text[:100])

        result = parse_response(content)
        ok, missing = validate_result(
            result,
            need_answer=task["need_answer"],
            need_discuss=task["need_discuss"],
        )
        if not ok:
            # 极少数情况：答案混在思维链而非 content，尝试兜底解析
            if reasoning_text:
                result2 = parse_response(reasoning_text)
                ok2, _ = validate_result(
                    result2,
                    need_answer=task["need_answer"],
                    need_discuss=task["need_discuss"],
                )
                if ok2:
                    logger.info("从 reasoning_content 中成功提取结果")
                    return result2
            logger.warning("AI 结果缺少字段 %s  raw=%s", missing, content[:200])
            return None

        return result

    # ─────────────────────────────── 回填内存 ───────────────────────────────

    def _apply_results(self, questions: list[Question], tasks: list[dict[str, Any]]) -> int:
        index  = {t["task_id"]: t for t in tasks}
        filled = 0
        for task_id, result in self._ckpt.results.items():
            if not result:
                continue
            t = index.get(task_id)
            if not t:
                continue
            q  = questions[t["qi"]]
            sq = q.sub_questions[t["si"]]
            apply_to_subquestion(sq, result, model_name=self.model, overwrite=self.apply_ai)
            filled += 1
        logger.info("回填完成: %d 个小题", filled)
        return filled


# ═══════════════════════════════════════════════════════════════════════
#  JSON 写回
# ═══════════════════════════════════════════════════════════════════════

def _write_back_json(questions: list[Question]) -> int:
    """
    将 ai_answer/ai_discuss 写回原始 JSON 文件（仅填空字段，不覆盖已有内容）。

    结构映射：
      A1/A2 型  → 顶层 answer / discuss
      A3/A4/B 型 → sub_questions[i].answer / sub_questions[i].discuss
    """
    file_groups: dict[str, list[Question]] = defaultdict(list)
    for q in questions:
        if q.source_file:
            file_groups[q.source_file].append(q)

    written = skipped = 0

    for file_path, qs in file_groups.items():
        fp = Path(file_path)
        if not fp.exists():
            logger.warning("源文件不存在，跳过: %s", fp)
            skipped += 1
            continue

        try:
            raw: dict = json.loads(fp.read_text(encoding="utf-8"))
        except Exception as e:
            logger.warning("读取 JSON 失败 %s: %s", fp, e)
            skipped += 1
            continue

        changed = False

        for q in qs:
            mode = q.mode or ""
            use_sub = (
                "A3" in mode or "A4" in mode or "案例" in mode
                or ("B" in mode and "型题" in mode)
            )

            for si, sq in enumerate(q.sub_questions):
                ai_ans = (sq.ai_answer  or "").strip()
                ai_dis = (sq.ai_discuss or "").strip()
                if not ai_ans and not ai_dis:
                    continue

                if use_sub:
                    subs = raw.get("sub_questions", [])
                    if si >= len(subs):
                        logger.warning("sub_questions 越界 %s si=%d", fp.name, si)
                        continue
                    node = subs[si]
                    if ai_ans and not (node.get("answer") or "").strip():
                        node["answer"] = ai_ans
                        changed = True
                    if ai_dis and not (node.get("discuss") or "").strip():
                        node["discuss"] = ai_dis
                        changed = True
                else:
                    if ai_ans and not (raw.get("answer") or "").strip():
                        raw["answer"] = ai_ans
                        changed = True
                    if ai_dis and not (raw.get("discuss") or "").strip():
                        raw["discuss"] = ai_dis
                        changed = True

        if changed:
            fp.write_text(
                json.dumps(raw, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
            written += 1
            logger.debug("已写回: %s", fp.name)

    if skipped:
        print(f"  ⚠️  {skipped} 个文件跳过（不存在或无法读取）")

    return written