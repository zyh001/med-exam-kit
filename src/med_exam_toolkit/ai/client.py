from __future__ import annotations

import logging
from typing import Any

logger = logging.getLogger(__name__)

# 各 provider 的默认 base_url
_PROVIDER_BASE_URLS: dict[str, str] = {
    "openai":   "https://api.openai.com/v1",
    "deepseek": "https://api.deepseek.com/v1",
    "ollama":   "http://localhost:11434/v1",
}

# 各 provider 的推荐默认模型
_PROVIDER_DEFAULT_MODELS: dict[str, str] = {
    "openai":   "gpt-4o",
    "deepseek": "deepseek-chat",
    "ollama":   "qwen2.5:14b",
}


def make_client(
    provider: str = "openai",
    api_key:  str = "",
    base_url: str = "",
    model:    str = "",          # 仅用于日志，不保存到客户端
    timeout:  float = 60.0,
) -> Any:
    """
    创建并返回 openai.OpenAI 客户端实例。

    Parameters
    ----------
    provider:
        ai 提供商名称（openai / deepseek / ollama）。
    api_key:
        API 密钥；ollama 本地部署时可留空。
    base_url:
        自定义接口地址；留空则使用 provider 默认值。
    model:
        仅用于日志输出，不写入客户端。
    timeout:
        HTTP 请求超时秒数。

    Returns
    -------
    openai.OpenAI
        可直接调用 .chat.completions.create() 的客户端。

    Raises
    ------
    RuntimeError
        openai 包未安装时抛出。
    ValueError
        未知 provider 时抛出。
    """
    try:
        from openai import OpenAI
    except ImportError:
        raise RuntimeError(
            "AI 功能需要安装 openai 包：pip install 'med-exam-toolkit[ai]'"
        )

    provider = provider.lower().strip()
    if provider not in _PROVIDER_BASE_URLS and not base_url:
        raise ValueError(
            f"未知 provider: {provider!r}，"
            f"可选: {list(_PROVIDER_BASE_URLS)}"
        )

    resolved_base_url = base_url or _PROVIDER_BASE_URLS.get(provider, "")
    resolved_model    = model    or _PROVIDER_DEFAULT_MODELS.get(provider, "")

    # ollama 本地部署不需要真实 key
    resolved_key = api_key or ("ollama" if provider == "ollama" else "sk-placeholder")

    logger.info(
        "AI 客户端: provider=%s  model=%s  base_url=%s",
        provider, resolved_model, resolved_base_url,
    )

    return OpenAI(
        api_key  = resolved_key,
        base_url = resolved_base_url,
        timeout  = timeout,
    )


def default_model(provider: str) -> str:
    """返回指定 provider 的推荐默认模型名称"""
    return _PROVIDER_DEFAULT_MODELS.get(provider.lower(), "gpt-4o")
