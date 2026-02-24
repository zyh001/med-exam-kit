from __future__ import annotations

import logging
from typing import Any

logger = logging.getLogger(__name__)

# 各 provider 的默认 base_url
_PROVIDER_BASE_URLS: dict[str, str] = {
    "openai":    "https://api.openai.com/v1",
    "deepseek":  "https://api.deepseek.com/v1",
    "ollama":    "http://localhost:11434/v1",
    "qwen":      "https://dashscope.aliyuncs.com/compatible-mode/v1",
    "qwen-intl": "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
}

# 各 provider 的推荐默认模型
_PROVIDER_DEFAULT_MODELS: dict[str, str] = {
    "openai":    "gpt-4o",
    "deepseek":  "deepseek-chat",
    "ollama":    "qwen2.5:14b",
    "qwen":      "qwen-plus",
    "qwen-intl": "qwen-plus",
}

# ── 模型分类 ──

# 1. 纯推理模型（始终走推理模式，不可关闭）
_PURE_REASONING_KEYWORDS = (
    "o1", "o3",
    "deepseek-reasoner",
    "-r1",
)

# 2. 混合思考模型（通过 enable_thinking 开关控制，默认关闭）
_HYBRID_THINKING_KEYWORDS = (
    "qwen3.5",
    "qwen-plus",
    "qwen3.5-plus",
)


def is_reasoning_model(model: str) -> bool:
    """纯推理模型：始终使用推理模式（o1/o3/DeepSeek-R1 等）"""
    m = model.lower()
    return any(kw in m for kw in _PURE_REASONING_KEYWORDS)


def is_hybrid_thinking_model(model: str) -> bool:
    """混合思考模型：支持 enable_thinking 开关（Qwen3 等）"""
    m = model.lower()
    return any(kw in m for kw in _HYBRID_THINKING_KEYWORDS)


def make_client(
    provider: str  = "openai",
    api_key:  str  = "",
    base_url: str  = "",
    model:    str  = "",
    timeout:  float = 120.0,
) -> Any:
    try:
        from openai import OpenAI
    except ImportError:
        raise RuntimeError(
            "AI 功能需要安装 openai 包：pip install 'med-exam-toolkit[ai]'"
        )

    provider = provider.lower().strip()
    if provider not in _PROVIDER_BASE_URLS and not base_url:
        raise ValueError(
            f"未知 provider: {provider!r}，可选: {list(_PROVIDER_BASE_URLS)}"
        )

    resolved_base_url = base_url or _PROVIDER_BASE_URLS.get(provider, "")
    resolved_model    = model    or _PROVIDER_DEFAULT_MODELS.get(provider, "")
    resolved_key      = api_key  or ("ollama" if provider == "ollama" else "sk-placeholder")

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
    return _PROVIDER_DEFAULT_MODELS.get(provider.lower(), "gpt-4o")


def build_chat_params(
    model:          str,
    messages:       list[dict],
    temperature:    float = 0.2,
    max_tokens:     int   = 1200,
    enable_thinking: bool | None = None,
) -> dict:
    """
    根据模型类型构建 chat.completions.create 的参数字典。

    三类模型的差异：
    ┌──────────────────┬────────────┬──────────────────┬──────────────────────┐
    │                  │ 普通模型   │ 纯推理模型        │ 混合思考模型          │
    │                  │ (gpt-4o等) │ (o1/o3/R1)       │ (Qwen3等)            │
    ├──────────────────┼────────────┼──────────────────┼──────────────────────┤
    │ temperature      │ 任意值     │ DeepSeek须为1    │ 思考模式须为1，       │
    │                  │            │ o1系列须省略      │ 普通模式任意值        │
    │ max_tokens       │ 支持       │ o1用             │ 支持                  │
    │                  │            │ max_completion_  │                      │
    │                  │            │ tokens           │                      │
    │ system role      │ 支持       │ o1须合并进user   │ 支持                  │
    │ extra_body       │ 不需要     │ 不需要            │ enable_thinking开关  │
    └──────────────────┴────────────┴──────────────────┴──────────────────────┘

    参数说明
    --------
    enable_thinking : 控制混合思考模型的思考开关
        None  → 自动判断（混合模型默认关闭，纯推理模型忽略此参数）
        True  → 强制开启思考（混合模型生效，普通模型忽略）
        False → 强制关闭思考
    """
    m         = model.lower()
    pure_r    = is_reasoning_model(model)
    hybrid    = is_hybrid_thinking_model(model)

    # 混合模型：enable_thinking 为 None 时默认关闭（除非调用方显式开启）
    use_thinking = False
    if hybrid:
        use_thinking = bool(enable_thinking)  # None → False
    elif pure_r:
        use_thinking = True   # 纯推理模型始终推理

    params: dict = {"model": model, "messages": messages}

    if pure_r:
        # DeepSeek-R1：temperature 须为 1
        if "deepseek" in m or "-r1" in m:
            params["temperature"] = 1
            params["max_tokens"]  = max_tokens
        # OpenAI o1/o3：不支持 temperature，用 max_completion_tokens
        else:
            params["max_completion_tokens"] = max_tokens
    elif hybrid and use_thinking:
        # 混合模型开启思考时：temperature 须为 1
        params["temperature"] = 1
        params["max_tokens"]  = max_tokens
        params["extra_body"]  = {"enable_thinking": True}
    else:
        # 普通模式（含混合模型关闭思考时）
        params["temperature"] = temperature
        params["max_tokens"]  = max_tokens
        if hybrid:
            # 混合模型显式关闭，避免 API 端默认开启
            params["extra_body"] = {"enable_thinking": False}

    return params


def adapt_messages_for_reasoning(model: str, messages: list[dict]) -> list[dict]:
    """
    为 OpenAI o1/o3 调整 messages 格式（将 system 消息合并进第一条 user 消息）。
    DeepSeek-R1 和 Qwen3 支持 system role，不需要调整。
    """
    m = model.lower()
    if "deepseek" in m or "-r1" in m or is_hybrid_thinking_model(model):
        return messages

    system_parts = [msg["content"] for msg in messages if msg["role"] == "system"]
    user_msgs    = [msg for msg in messages if msg["role"] != "system"]

    if not system_parts:
        return user_msgs

    if user_msgs and user_msgs[0]["role"] == "user":
        prefix = "\n\n".join(system_parts)
        user_msgs[0] = {
            "role":    "user",
            "content": f"{prefix}\n\n{user_msgs[0]['content']}",
        }
    return user_msgs


def extract_response_text(response: Any) -> tuple[str, str]:
    """
    从 API 响应中提取 (content, reasoning_content)。

    统一处理各模型的响应差异：
    - 普通模型：content 有值，reasoning_content 为空
    - DeepSeek-R1 / Qwen3 思考模式：content 为最终答案，
      reasoning_content 为思维链（通过 message 属性或 extra 字段）
    """
    msg = response.choices[0].message

    content          = (msg.content or "").strip()
    reasoning_content = ""

    # 标准属性（DeepSeek-R1、Qwen3 均支持）
    if hasattr(msg, "reasoning_content") and msg.reasoning_content:
        reasoning_content = msg.reasoning_content

    return content, reasoning_content