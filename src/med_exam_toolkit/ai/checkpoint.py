"""断点续传管理"""
from __future__ import annotations

import json
import logging
import os
import tempfile
import threading
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Generator, Iterable, TypeVar

logger = logging.getLogger(__name__)

T = TypeVar("T")

class Checkpoint:

    _EXT = ".ckpt.json"

    def __init__(
        self,
        tag: str,
        checkpoint_dir: Path = Path("data/checkpoints"),
        id_fn: Callable[[Any], str] = str,
    ) -> None:
        self.tag           = tag
        self.checkpoint_dir = Path(checkpoint_dir)
        self._id_fn        = id_fn
        self._lock         = threading.Lock()

        # 核心状态
        self._completed: dict[str, Any] = {}   # id → result（None 表示调用失败）
        self._meta: dict[str, str]      = {}

    # ── 文件路径 ────────────────────────────────────────
    @property
    def path(self) -> Path:
        return self.checkpoint_dir / f"{self.tag}{self._EXT}"

    # ── 公开属性 ────────────────────────────────────────
    @property
    def results(self) -> dict[str, Any]:
        """返回 id → result 的只读视图"""
        return dict(self._completed)

    # ── 加载 ────────────────────────────────────────────
    def load(self) -> int:
        if not self.path.exists():
            logger.debug("断点文件不存在，全新开始: %s", self.path)
            return 0

        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
            self._completed = raw.get("completed", {})
            self._meta      = raw.get("meta", {})
            count = len(self._completed)
            logger.info("断点恢复: 已完成 %d 条  文件=%s", count, self.path)
            return count
        except (json.JSONDecodeError, OSError) as exc:
            logger.warning("断点文件损坏，忽略并重新开始: %s (%s)", self.path, exc)
            self._completed = {}
            return 0

    # ── 标记完成 ─────────────────────────────────────────
    def done(self, item_id: str, result: Any) -> None:
        with self._lock:
            self._completed[item_id] = result
            self._flush()

    # ── 查询 ────────────────────────────────────────────
    def is_done(self, item_id: str) -> bool:
        return item_id in self._completed

    # ── 迭代（跳过已完成） ───────────────────────────────
    def iter(self, items: Iterable[T]) -> Generator[T, None, None]:
        skipped = 0
        for item in items:
            item_id = self._id_fn(item)
            if self.is_done(item_id):
                skipped += 1
                continue
            yield item
        if skipped:
            logger.info("断点跳过: %d 条", skipped)

    # ── 清除 ────────────────────────────────────────────
    def clear(self) -> None:
        if self.path.exists():
            self.path.unlink()
            logger.info("断点文件已清除: %s", self.path)
        self._completed = {}
        self._meta      = {}

    # ── 私有：原子写盘 ───────────────────────────────────
    def _flush(self) -> None:
        self.checkpoint_dir.mkdir(parents=True, exist_ok=True)
        payload = {
            "meta": {
                "tag":      self.tag,
                "saved_at": datetime.now(timezone.utc).isoformat(),
            },
            "completed": self._completed,
        }

        # 在同一目录写临时文件，保证 rename 是原子操作
        fd, tmp_path = tempfile.mkstemp(
            dir    = self.checkpoint_dir,
            prefix = f".{self.tag}_",
            suffix = ".tmp",
        )
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump(payload, f, ensure_ascii=False, indent=2)
            os.replace(tmp_path, self.path)   # 原子 rename
        except OSError:
            # 写盘失败不能中断主流程，只记日志
            logger.exception("断点写盘失败: %s", self.path)
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)
