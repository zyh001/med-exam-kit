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
    "kimi":      "https://api.moonshot.cn/v1",
    "minimax":   "https://api.minimaxi.com/v1",
    "zhipu":     "https://open.bigmodel.cn/api/paas/v4",
}

# 各 provider 的推荐默认模型
_PROVIDER_DEFAULT_MODELS: dict[str, str] = {
    "openai":    "gpt-4o",
    "deepseek":  "deepseek-chat",
    "ollama":    "qwen2.5:14b",
    "qwen":      "qwen-plus",
    "qwen-intl": "qwen-plus",
    "kimi":      "kimi-k2.5",
    "minimax":   "MiniMax-M2.5",
    "zhipu":     "glm-5",
}

# ── 模型分类 ──

# 1. 纯推理模型（始终走推理模式，不可关闭）
_PURE_REASONING_KEYWORDS = (
    "o1", "o3", "o4",
    "deepseek-reasoner",
    "-r1",
    "kimi-k2-thinking",
)

# 2. 混合思考模型（通过 enable_thinking 开关控制，默认关闭）
# 匹配规则：按顺序匹配，使用关键词前缀匹配覆盖更多模型
_HYBRID_THINKING_KEYWORDS = (
    # Qwen3.5 系列（含 Plus/Flash 等变体）
    "qwen3.5-plus",
    "qwen3.5-flash",
    "qwen3.5",
    # Qwen3-Max 系列（由 qwen3 前缀覆盖）
    "qwen-max",      # 覆盖 qwen-max, qwen-max-latest, qwen-max-2025-01-25 等
    "qwen-plus",     # 覆盖 qwen-plus 及其所有版本
    "qwen3",         # 覆盖所有 qwen3-* 系列（含 qwen3-max, qwen3-coder 等）
    # Kimi-K2.5（支持 thinking 参数开关）
    "kimi-k2.5",
    # MiniMax 系列（混合推理模型，通过 reasoning_split 参数控制）
    "minimax",
    # GLM-5 系列（智谱 AI，通过 thinking 参数控制）
    "glm-5", "glm-4.7",
    # DeepSeek chat/v3/v4：API 默认 thinking=enabled，需显式关闭
    "deepseek-chat", "deepseek-v3", "deepseek-v4",
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


def chat_completion_stream(
    client: Any,
    model: str,
    messages: list[dict],
    temperature: float = 0.7,
    max_tokens: int = 2048,
    enable_thinking: bool | None = None,
    provider: str | None = None,
):
    """
    Generator that yields dicts: {"content": str, "reasoning": str}
    for each SSE chunk from the OpenAI-compatible streaming API.
    Yields {"done": True} at the end, or {"error": str} on failure.
    """
    import re

    params = build_chat_params(
        model=model,
        messages=messages,
        temperature=temperature,
        max_tokens=max_tokens,
        enable_thinking=enable_thinking,
        provider=provider,
    )
    params["stream"] = True

    # extract extra_body if present (openai SDK uses it as a kwarg)
    extra_body = params.pop("extra_body", None)

    try:
        stream = client.chat.completions.create(**params, extra_body=extra_body)
    except Exception as e:
        yield {"error": str(e)}
        return

    try:
        for chunk in stream:
            if not chunk.choices:
                continue
            delta = chunk.choices[0].delta
            content = getattr(delta, "content", None) or ""
            reasoning = getattr(delta, "reasoning_content", None) or ""

            # MiniMax: reasoning may come from reasoning_details
            if not reasoning and hasattr(delta, "reasoning_details") and delta.reasoning_details:
                for detail in delta.reasoning_details:
                    if hasattr(detail, "text") and detail.text:
                        reasoning = detail.text
                        break

            # MiniMax fallback: extract <think> from content
            if not reasoning and content:
                m = re.search(r'<think>(.*?)</think>', content, re.DOTALL)
                if m:
                    reasoning = m.group(1)
                    content = re.sub(r'<think>.*?</think>', '', content, flags=re.DOTALL)

            if content or reasoning:
                yield {"content": content, "reasoning": reasoning}

            # finish_reason=length 说明被 max_tokens 截断
            finish_reason = getattr(chunk.choices[0], "finish_reason", None)
            if finish_reason == "length":
                yield {"truncated": True}
                yield {"done": True}
                return

        yield {"done": True}
    except Exception as e:
        yield {"error": str(e)}


def build_chat_params(
    model:          str,
    messages:       list[dict],
    temperature:    float = 0.2,
    max_tokens:     int   = 1200,
    enable_thinking: bool | None = None,
    provider:       str | None = None,
) -> dict:
    """
    根据模型类型构建 chat.completions.create 的参数字典。

    三类模型的差异：
    ┌──────────────────┬────────────┬──────────────────┬──────────────────────┐
    │                  │ 普通模型   │ 纯推理模型        │ 混合思考模型          │
    │                  │ (gpt-4o 等) │ (o1/o3/R1)       │ (Qwen3 等)            │
    ├──────────────────┼────────────┼──────────────────┼──────────────────────┤
    │ temperature      │ 任意值     │ DeepSeek 须为 1    │ 思考模式须为 1，       │
    │                  │            │ o1 系列须省略      │ 普通模式任意值        │
    │ max_tokens       │ 支持       │ o1 用             │ 支持                  │
    │                  │            │ max_completion_  │                      │
    │                  │            │ tokens           │                      │
    │ system role      │ 支持       │ o1 须合并进 user   │ 支持                  │
    │ extra_body       │ 不需要     │ 不需要            │ 思考参数（因 provider 而异）│
    └──────────────────┴────────────┴──────────────────┴──────────────────────┘

    参数说明
    --------
    enable_thinking : 控制混合思考模型的思考开关
        None  → 自动判断（混合模型默认关闭，纯推理模型忽略此参数）
        True  → 强制开启思考（混合模型生效，普通模型忽略）
        False → 强制关闭思考
    provider : 模型提供商名称（qwen/kimi/minimax 等），用于确定思考参数的格式
    """
    m         = model.lower()
    pure_r    = is_reasoning_model(model)
    hybrid    = is_hybrid_thinking_model(model)
    provider  = (provider or "").lower()

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
        # 混合模型开启思考时：temperature 须为 1（DeepSeek thinking 模式不支持 temperature，省略）
        if provider != "deepseek":
            params["temperature"] = 1
        params["max_tokens"]  = max_tokens
        # 根据 provider 使用不同的思考参数格式
        if provider in ("kimi", "zhipu", "deepseek"):
            # 月之暗面 Kimi / 智谱 GLM / DeepSeek：{"thinking": {"type": "enabled"}}
            params["extra_body"] = {"thinking": {"type": "enabled"}}
        elif provider == "minimax":
            # MiniMax：{"reasoning_split": True}
            params["extra_body"] = {"reasoning_split": True}
        else:
            # 阿里系 Qwen（默认）：{"enable_thinking": True}
            params["extra_body"] = {"enable_thinking": True}
    else:
        # 普通模式（含混合模型关闭思考时）
        params["temperature"] = temperature
        params["max_tokens"]  = max_tokens
        if hybrid:
            # 混合模型显式关闭，避免 API 端默认开启
            # 根据 provider 使用不同的思考参数格式
            if provider in ("kimi", "zhipu", "deepseek"):
                # 月之暗面 Kimi / 智谱 GLM / DeepSeek：{"thinking": {"type": "disabled"}}
                params["extra_body"] = {"thinking": {"type": "disabled"}}
            elif provider == "minimax":
                # MiniMax：{"reasoning_split": False}
                params["extra_body"] = {"reasoning_split": False}
            else:
                # 阿里系 Qwen（默认）：{"enable_thinking": False}
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
    - MiniMax 混合模型：
      - reasoning_split=True: reasoning_content 来自 reasoning_details 字段
      - reasoning_split=False: 从 content 中提取 <thought> 标签内容
    """
    import re
    
    msg = response.choices[0].message

    content          = (msg.content or "").strip()
    reasoning_content = ""

    # 1. 标准属性（DeepSeek-R1、Qwen3 均支持）
    if hasattr(msg, "reasoning_content") and msg.reasoning_content:
        reasoning_content = msg.reasoning_content

    # 2. MiniMax 特殊格式：reasoning_details 字段
    if hasattr(msg, "reasoning_details") and msg.reasoning_details:
        # reasoning_details 是一个列表，每个元素包含 type 和 text 字段
        for detail in msg.reasoning_details:
            if hasattr(detail, "text") and detail.text:
                reasoning_content = detail.text
                break
    
    # 3. MiniMax 原生格式：从 content 中提取 <think> 标签内容
    # 当 reasoning_split=False 时，思维链包裹在 <think>...</think> 标签中
    if not reasoning_content and content:
        thought_match = re.search(r'<think>(.*?)</think>', content, re.DOTALL)
        if thought_match:
            reasoning_content = thought_match.group(1).strip()
            # 从 content 中移除 <thought> 标签，只保留最终答案
            content = re.sub(r'<think>.*?</think>', '', content, flags=re.DOTALL).strip()

    return content, reasoning_content
