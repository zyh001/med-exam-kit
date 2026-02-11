from __future__ import annotations
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .base import BaseParser

_REGISTRY: dict[str, type[BaseParser]] = {}


def register(name: str):
    """装饰器：注册解析器"""
    def wrapper(cls):
        _REGISTRY[name] = cls
        return cls
    return wrapper


def get_parser(name: str) -> BaseParser:
    if name not in _REGISTRY:
        raise KeyError(f"未注册的解析器: {name}，已注册: {list(_REGISTRY)}")
    return _REGISTRY[name]()


def discover():
    """导入所有内置解析器，触发 @register"""
    from . import ahuyikao, yikaobang  # noqa: F401
