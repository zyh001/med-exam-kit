from __future__ import annotations
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .base import BaseExporter

_REGISTRY: dict[str, type[BaseExporter]] = {}


def register(name: str):
    def wrapper(cls):
        _REGISTRY[name] = cls
        return cls
    return wrapper


def get_exporter(name: str) -> BaseExporter:
    if name not in _REGISTRY:
        raise KeyError(f"未注册的导出器: {name}，已注册: {list(_REGISTRY)}")
    return _REGISTRY[name]()


def discover():
    from . import csv_exporter, xlsx_exporter, docx_exporter, pdf_exporter, db_exporter  # noqa: F401
