"""AI 费用估算与 Token 统计

用法：
    from med_exam_toolkit.ai.cost import CostTracker, estimate_prompt_tokens

    tracker = CostTracker(model="gpt-4o")
    tracker.add(prompt_tokens=500, completion_tokens=200)
    print(tracker.summary())
"""
from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import NamedTuple


# ════════════════════════════════════════════════════════════════════════
# 定价表（每 1M tokens 的美元价格，2026 年初公开定价，仅供估算参考）
# 格式：{ 模糊匹配前缀/关键词：(input_per_1M, output_per_1M) }
# 匹配规则：按 PRICING 顺序，第一个 key in model.lower() 则命中
# ════════════════════════════════════════════════════════════════════════

class _Price(NamedTuple):
    input: float   # USD per 1M input tokens
    output: float  # USD per 1M output tokens
    note: str = ""

# 按匹配优先级排列（越具体地放越前面）
_PRICING: list[tuple[str, _Price]] = [
    # ── Ollama（本地，免费）── 放在所有云端模型之前，防止被 qwen 前缀抢匹配
    ("ollama",             _Price( 0.00,    0.00, "Ollama 本地部署")),
    
    # ── OpenAI ──
    # 价格来源：OpenAI 官方定价（https://developers.openai.com/api/docs/pricing）
    # 说明：使用标准输入价格（Short context），不考虑缓存折扣
    
    # ── GPT-5 系列（最新模型）──
    ("gpt-5.4-pro",        _Price(30.00, 180.00, "GPT-5.4 Pro")),
    ("gpt-5.4",            _Price( 2.50,  15.00, "GPT-5.4")),
    ("gpt-5.3-codex",      _Price( 1.75,  14.00, "GPT-5.3 Codex")),
    ("gpt-5.3-chat-latest",_Price( 1.75,  14.00, "GPT-5.3 Chat")),
    ("gpt-5.2-pro",        _Price(21.00, 168.00, "GPT-5.2 Pro")),
    ("gpt-5.2-codex",      _Price( 1.75,  14.00, "GPT-5.2 Codex")),
    ("gpt-5.2",            _Price( 1.75,  14.00, "GPT-5.2")),
    ("gpt-5.2-chat-latest",_Price( 1.75,  14.00, "GPT-5.2 Chat")),
    ("gpt-5.1-codex-max",  _Price( 1.25,  10.00, "GPT-5.1 Codex Max")),
    ("gpt-5.1-codex-mini", _Price( 0.25,   2.00, "GPT-5.1 Codex Mini")),
    ("gpt-5.1-codex",      _Price( 1.25,  10.00, "GPT-5.1 Codex")),
    ("gpt-5.1-chat-latest",_Price( 1.25,  10.00, "GPT-5.1 Chat")),
    ("gpt-5.1",            _Price( 1.25,  10.00, "GPT-5.1")),
    ("gpt-5-codex",        _Price( 1.25,  10.00, "GPT-5 Codex")),
    ("gpt-5-chat-latest",  _Price( 1.25,  10.00, "GPT-5 Chat")),
    ("gpt-5-pro",          _Price(15.00, 120.00, "GPT-5 Pro")),
    ("gpt-5-mini",         _Price( 0.25,   2.00, "GPT-5 Mini")),
    ("gpt-5-nano",         _Price( 0.05,   0.40, "GPT-5 Nano")),
    ("gpt-5",              _Price( 1.25,  10.00, "GPT-5")),
    ("gpt-5-search-api",   _Price( 1.25,  10.00, "GPT-5 Search API")),
    
    # ── GPT-4.1 系列 ──
    ("gpt-4.1",            _Price( 2.00,   8.00, "GPT-4.1")),
    ("gpt-4.1-mini",       _Price( 0.40,   1.60, "GPT-4.1 Mini")),
    ("gpt-4.1-nano",       _Price( 0.10,   0.40, "GPT-4.1 Nano")),
    
    # ── GPT-4o 系列 ──
    ("gpt-4o-2024-05-13",  _Price( 5.00,  15.00, "GPT-4o (2024-05-13)")),
    ("gpt-4o",             _Price( 2.50,  10.00, "GPT-4o")),
    ("gpt-4o-mini",        _Price( 0.15,   0.60, "GPT-4o Mini")),
    ("gpt-4o-audio-preview", _Price(2.50, 10.00, "GPT-4o Audio Preview")),
    ("gpt-4o-mini-audio-preview", _Price(0.15, 0.60, "GPT-4o Mini Audio Preview")),
    ("gpt-4o-realtime-preview", _Price(5.00, 20.00, "GPT-4o Realtime Preview")),
    ("gpt-4o-mini-realtime-preview", _Price(0.60, 2.40, "GPT-4o Mini Realtime Preview")),
    ("gpt-4o-search-preview", _Price(2.50, 10.00, "GPT-4o Search Preview")),
    ("gpt-4o-mini-search-preview", _Price(0.15, 0.60, "GPT-4o Mini Search Preview")),
    
    # ── GPT-Realtime 系列 ──
    ("gpt-realtime",       _Price( 4.00,  16.00, "GPT Realtime")),
    ("gpt-realtime-1.5",   _Price( 4.00,  16.00, "GPT Realtime 1.5")),
    ("gpt-realtime-mini",  _Price( 0.60,   2.40, "GPT Realtime Mini")),
    
    # ── GPT-Audio 系列 ──
    ("gpt-audio",          _Price( 2.50,  10.00, "GPT Audio")),
    ("gpt-audio-1.5",      _Price( 2.50,  10.00, "GPT Audio 1.5")),
    ("gpt-audio-mini",     _Price( 0.60,   2.40, "GPT Audio Mini")),
    
    # ── o 系列（推理模型）──
    ("o1-pro",             _Price(150.00, 600.00, "o1 Pro")),
    ("o1",                 _Price( 15.00,  60.00, "o1")),
    ("o1-mini",            _Price(  1.10,   4.40, "o1 Mini")),
    ("o3-pro",             _Price( 20.00,  80.00, "o3 Pro")),
    ("o3",                 _Price(  2.00,   8.00, "o3")),
    ("o3-deep-research",   _Price( 10.00,  40.00, "o3 Deep Research")),
    ("o3-mini",            _Price(  1.10,   4.40, "o3 Mini")),
    ("o4-mini",            _Price(  1.10,   4.40, "o4 Mini")),
    ("o4-mini-deep-research", _Price(2.00, 8.00, "o4 Mini Deep Research")),
    
    # ── Codex 系列 ──
    ("codex-mini-latest",  _Price( 1.50,   6.00, "Codex Mini Latest")),

    # ── Computer Use ──
    ("computer-use-preview", _Price(3.00, 12.00, "Computer Use Preview")),
    
    # ── GPT-4 Turbo（旧版）──
    ("gpt-4-turbo",        _Price(10.00,  30.00, "GPT-4 Turbo")),
    ("gpt-4",              _Price(30.00,  60.00, "GPT-4")),
    
    # ── GPT-3.5（旧版）──
    ("gpt-3.5",            _Price( 0.50,   1.50, "GPT-3.5 Turbo")),
    
    # ── DeepSeek ──
    # 价格来源：DeepSeek 官方定价（https://api-docs.deepseek.com/zh-cn/quick_start/pricing）
    # 模型版本：DeepSeek-V3.2（128K context）
    # 说明：使用缓存未命中价格（cache miss），缓存命中价格为 $0.028/1M（约 9 折优惠）
    ("deepseek-reasoner",  _Price( 0.28,    0.42, "DeepSeek-V3.2 Thinking (cache miss)")),
    ("deepseek-chat",      _Price( 0.28,    0.42, "DeepSeek-V3.2 Non-thinking (cache miss)")),
    
    # ── Kimi / 月之暗面 ──
    # 汇率说明：CNY 价格 / 6.9 = USD 价格
    # 价格来源：月之暗面官方定价（https://platform.moonshot.cn/docs/pricing/chat）
    # 说明：kimi-k2.5 支持 thinking 参数，kimi-k2-thinking 为纯思考模型
    
    # ── Kimi-K2.5（多模态模型，支持思考开关）──
    ("kimi-k2.5",          _Price( 0.58,    3.04, "Kimi-K2.5 (CNY4/21)")),
    
    # ── Kimi-K2 系列（含 preview/turbo 变体）──
    ("kimi-k2-turbo-preview", _Price(1.16, 8.41, "Kimi-K2-Turbo-Preview (CNY8/58)")),
    ("kimi-k2-thinking-turbo", _Price(1.16, 8.41, "Kimi-K2-Thinking-Turbo (CNY8/58)")),
    ("kimi-k2-0905-preview", _Price(0.58,   2.32, "Kimi-K2-0905-Preview (CNY4/16)")),
    ("kimi-k2-0711-preview", _Price(0.58,   2.32, "Kimi-K2-0711-Preview (CNY4/16)")),
    ("kimi-k2-thinking",   _Price( 0.58,    2.32, "Kimi-K2-Thinking (CNY4/16)")),
    ("kimi-k2",            _Price( 0.58,    2.32, "Kimi-K2 (CNY4/16)")),
    
    # ── Moonshot-V1 系列（通用生成模型）──
    ("moonshot-v1-128k-vision-preview", _Price(1.45, 4.35, "Moonshot-V1-128K-Vision-Preview (CNY10/30)")),
    ("moonshot-v1-32k-vision-preview",  _Price(0.72, 2.90, "Moonshot-V1-32K-Vision-Preview (CNY5/20)")),
    ("moonshot-v1-8k-vision-preview",   _Price(0.29, 1.45, "Moonshot-V1-8K-Vision-Preview (CNY2/10)")),
    ("moonshot-v1-128k", _Price( 1.45,    4.35, "Moonshot-V1-128K (CNY10/30)")),
    ("moonshot-v1-32k",  _Price( 0.72,    2.90, "Moonshot-V1-32K (CNY5/20)")),
    ("moonshot-v1-8k",   _Price( 0.29,    1.45, "Moonshot-V1-8K (CNY2/10)")),
    
    # ── MiniMax / 月之暗面 ──
    # 汇率说明：CNY 价格 / 6.9 = USD 价格
    # 价格来源：MiniMax 官方定价（https://platform.minimaxi.com/docs/guides/pricing-paygo）
    # 说明：MiniMax 模型为混合思考模型，通过 reasoning_split 参数控制思维链分离
    # 缓存价格：读取 ¥0.21/1M, 写入 ¥2.625/1M（未单独列出，按标准价格计算）
    
    # ── MiniMax-M2.5 系列（高性能模型）──
    ("minimax-m2.5-highspeed", _Price(0.61, 2.43, "MiniMax-M2.5-Highspeed (CNY4.2/16.8)")),
    ("minimax-m2.5",           _Price(0.30, 1.22, "MiniMax-M2.5 (CNY2.1/8.4)")),
    
    # ── MiniMax-M2.1 系列（编程与多语言）──
    ("minimax-m2.1-highspeed", _Price(0.61, 2.43, "MiniMax-M2.1-Highspeed (CNY4.2/16.8)")),
    ("minimax-m2.1",           _Price(0.30, 1.22, "MiniMax-M2.1 (CNY2.1/8.4)")),
    
    # ── MiniMax-M2（编码与 Agent）──
    ("minimax-m2",             _Price(0.30, 1.22, "MiniMax-M2 (CNY2.1/8.4)")),
    
    # ── Qwen / 通义千问 ──
    # 汇率说明：CNY 价格 / 6.9 = USD 价格
    # - 价格来源：阿里云百炼官方定价（https://help.aliyun.com/zh/model-studio/model-pricing）
    # - 阶梯计价：统一按最低档位计算（如 0<Token≤32K 档）
    # - Batch 调用/缓存折扣：未计入，按标准价格计算
    
    # ── Qwen-Max 系列（高端模型）──
    ("qwen3-max-preview",  _Price( 0.87,    3.48, "Qwen3-Max-Preview (CNY6/24)")),
    ("qwen3-max-2025-09-23", _Price(0.87,   3.48, "Qwen3-Max-2025-09-23 (CNY6/24)")),
    ("qwen3-max-2026-01-23", _Price(0.36,   1.45, "Qwen3-Max-2026-01-23 (CNY2.5/10)")),
    ("qwen3-max",          _Price( 0.36,    1.45, "Qwen3-Max (CNY2.5/10)")),
    ("qwen-max-2024-09-19", _Price( 2.90,   8.70, "Qwen-Max-2024-09-19 (CNY20/60)")),
    ("qwen-max-2024-04-28", _Price( 5.80,  17.39, "Qwen-Max-2024-04-28 (CNY40/120)")),
    ("qwen-max-2025-01-25", _Price( 0.35,    1.39, "Qwen-Max-2025-01-25 (CNY2.4/9.6)")),
    ("qwen-max-latest",    _Price( 0.35,    1.39, "Qwen-Max-Latest (CNY2.4/9.6)")),
    ("qwen-max",           _Price( 0.35,    1.39, "Qwen-Max (CNY2.4/9.6)")),
    
    # ── Qwen-Plus 系列（中端模型）──
    ("qwen3.5-plus-2026-02-15", _Price(0.12, 0.70, "Qwen3.5-Plus-2026-02-15 (CNY0.8/4.8)")),
    ("qwen3.5-plus",       _Price( 0.12,    0.70, "Qwen3.5-Plus (CNY0.8/4.8)")),
    ("qwen-plus-2025-12-01", _Price(0.12,   1.16, "Qwen-Plus-2025-12-01 (CNY0.8/8)")),
    ("qwen-plus-2025-09-11", _Price(0.12,   1.16, "Qwen-Plus-2025-09-11 (CNY0.8/8)")),
    ("qwen-plus-2025-07-28", _Price(0.12,   1.16, "Qwen-Plus-2025-07-28 (CNY0.8/8)")),
    ("qwen-plus-2025-07-14", _Price(0.12,   1.16, "Qwen-Plus-2025-07-14 (CNY0.8/8)")),
    ("qwen-plus-2025-04-28", _Price(0.12,   1.16, "Qwen-Plus-2025-04-28 (CNY0.8/8)")),
    ("qwen-plus-2025-01-25", _Price(0.12,   1.16, "Qwen-Plus-2025-01-25 (CNY0.8/2)")),
    ("qwen-plus-2025-01-12", _Price(0.12,   1.16, "Qwen-Plus-2025-01-12 (CNY0.8/2)")),
    ("qwen-plus-2024-12-20", _Price(0.12,   1.16, "Qwen-Plus-2024-12-20 (CNY0.8/2)")),
    ("qwen-plus-latest",   _Price( 0.12,    1.16, "Qwen-Plus-Latest (CNY0.8/8)")),
    ("qwen-plus",          _Price( 0.12,    1.16, "Qwen-Plus (CNY0.8/8)")),
    
    # ── Qwen-Flash 系列（快速模型）──
    ("qwen3.5-flash-2026-02-23", _Price(0.03, 0.29, "Qwen3.5-Flash-2026-02-23 (CNY0.2/2)")),
    ("qwen3.5-flash",      _Price( 0.03,    0.29, "Qwen3.5-Flash (CNY0.2/2)")),
    ("qwen-flash-2025-07-28", _Price(0.02,  0.22, "Qwen-Flash-2025-07-28 (CNY0.15/1.5)")),
    ("qwen-flash",         _Price( 0.02,    0.22, "Qwen-Flash (CNY0.15/1.5)")),
    
    # ── Qwen-Turbo 系列（极速模型）──
    ("qwen-turbo-latest",  _Price( 0.04,    0.43, "Qwen-Turbo-Latest (CNY0.3/3)")),
    ("qwen-turbo-2025-07-15", _Price(0.04,  0.43, "Qwen-Turbo-2025-07-15 (CNY0.3/3)")),
    ("qwen-turbo-2025-04-28", _Price(0.04,  0.43, "Qwen-Turbo-2025-04-28 (CNY0.3/3)")),
    ("qwen-turbo-2025-02-11", _Price(0.04,  0.43, "Qwen-Turbo-2025-02-11 (CNY0.3/0.6)")),
    ("qwen-turbo-2024-11-01", _Price(0.04,  0.43, "Qwen-Turbo-2024-11-01 (CNY0.3/0.6)")),
    ("qwen-turbo",         _Price( 0.04,    0.43, "Qwen-Turbo (CNY0.3/3)")),
    
    # ── Qwen-Long（长文本模型）──
    ("qwen-long-latest",   _Price( 0.07,    0.29, "Qwen-Long-Latest (CNY0.5/2)")),
    ("qwen-long-2025-01-25", _Price(0.07,   0.29, "Qwen-Long-2025-01-25 (CNY0.5/2)")),
    ("qwen-long",          _Price( 0.07,    0.29, "Qwen-Long (CNY0.5/2)")),
    
    # ── QwQ（推理模型）──
    ("qwq-plus-latest",    _Price( 0.23,    0.58, "QwQ-Plus-Latest (CNY1.6/4)")),
    ("qwq-plus-2025-03-05", _Price(0.23,    0.58, "QwQ-Plus-2025-03-05 (CNY1.6/4)")),
    ("qwq-plus",           _Price( 0.23,    0.58, "QwQ-Plus (CNY1.6/4)")),
    ("qwq-32b",            _Price( 0.29,    0.87, "QwQ-32B (CNY2/6)")),
    ("qwq-32b-preview",    _Price( 0.29,    0.87, "QwQ-32B-Preview (CNY2/6)")),

    # ── Qwen-Coder（代码模型）──
    ("qwen3-coder-plus-2025-09-23", _Price(0.58, 2.32, "Qwen3-Coder-Plus-2025-09-23 (CNY4/16)")),
    ("qwen3-coder-plus-2025-07-22", _Price(0.58, 2.32, "Qwen3-Coder-Plus-2025-07-22 (CNY4/16)")),
    ("qwen3-coder-plus",   _Price( 0.58,    2.32, "Qwen3-Coder-Plus (CNY4/16)")),
    ("qwen3-coder-flash",  _Price( 0.14,    0.58, "Qwen3-Coder-Flash (CNY1/4)")),
    ("qwen3-coder-flash-2025-07-28", _Price(0.14, 0.58, "Qwen3-Coder-Flash-2025-07-28 (CNY1/4)")),
    ("qwen-coder-plus-latest", _Price(0.51, 1.01, "Qwen-Coder-Plus-Latest (CNY3.5/7)")),
    ("qwen-coder-plus-2024-11-06", _Price(0.51, 1.01, "Qwen-Coder-Plus-2024-11-06 (CNY3.5/7)")),
    ("qwen-coder-plus",    _Price( 0.51,    1.01, "Qwen-Coder-Plus (CNY3.5/7)")),
    ("qwen-coder-turbo-latest", _Price(0.29, 0.87, "Qwen-Coder-Turbo-Latest (CNY2/6)")),
    ("qwen-coder-turbo-2024-09-19", _Price(0.29, 0.87, "Qwen-Coder-Turbo-2024-09-19 (CNY2/6)")),
    ("qwen-coder-turbo",   _Price( 0.29,    0.87, "Qwen-Coder-Turbo (CNY2/6)")),
    
    # ── Qwen-Omni（多模态模型）──
    ("qwen3-omni-flash-2025-12-01", _Price(0.26, 1.00, "Qwen3-Omni-Flash-2025-12-01 (CNY1.8/6.9)")),
    ("qwen3-omni-flash-2025-09-15", _Price(0.26, 1.00, "Qwen3-Omni-Flash-2025-09-15 (CNY1.8/6.9)")),
    ("qwen3-omni-flash",   _Price( 0.26,    1.00, "Qwen3-Omni-Flash (CNY1.8/6.9)")),
    ("qwen-omni-turbo-latest", _Price(0.06, 0.65, "Qwen-Omni-Turbo-Latest (CNY0.4/4.5)")),
    ("qwen-omni-turbo-2025-03-26", _Price(0.06, 0.65, "Qwen-Omni-Turbo-2025-03-26 (CNY0.4/4.5)")),
    ("qwen-omni-turbo-2025-01-19", _Price(0.06, 0.65, "Qwen-Omni-Turbo-2025-01-19 (CNY0.4/4.5)")),
    ("qwen-omni-turbo",    _Price( 0.06,    0.65, "Qwen-Omni-Turbo (CNY0.4/4.5)")),

    # ── Qwen2.5 开源版 ──
    ("qwen2.5-14b-instruct-1m", _Price(0.14, 0.43, "Qwen2.5-14B-Instruct-1M (CNY1/3)")),
    ("qwen2.5-7b-instruct-1m",  _Price(0.07, 0.14, "Qwen2.5-7B-Instruct-1M (CNY0.5/1)")),
    ("qwen2.5-72b-instruct", _Price( 0.58,    1.74, "Qwen2.5-72B-Instruct (CNY4/12)")),
    ("qwen2.5-32b-instruct", _Price( 0.29,    0.87, "Qwen2.5-32B-Instruct (CNY2/6)")),
    ("qwen2.5-14b-instruct", _Price( 0.14,    0.43, "Qwen2.5-14B-Instruct (CNY1/3)")),
    ("qwen2.5-7b-instruct",  _Price( 0.07,    0.14, "Qwen2.5-7B-Instruct (CNY0.5/1)")),
    ("qwen2.5-3b-instruct",  _Price( 0.04,    0.13, "Qwen2.5-3B-Instruct (CNY0.3/0.9)")),
    
    # ── Qwen3 开源版 ──
    ("qwen3-235b-a22b-thinking-2507", _Price(0.29, 2.90, "Qwen3-235B-A22B-Thinking-2507 (CNY2/20)")),
    ("qwen3-235b-a22b-instruct-2507", _Price(0.29, 1.16, "Qwen3-235B-A22B-Instruct-2507 (CNY2/8)")),
    ("qwen3-30b-a3b-thinking-2507", _Price(0.11, 1.09, "Qwen3-30B-A3B-Thinking-2507 (CNY0.75/7.5)")),
    ("qwen3-30b-a3b-instruct-2507", _Price(0.11, 0.43, "Qwen3-30B-A3B-Instruct-2507 (CNY0.75/3)")),
    ("qwen3-235b-a22b",      _Price( 0.29,    1.16, "Qwen3-235B-A22B (CNY2/8)")),
    ("qwen3-32b",            _Price( 0.29,    1.16, "Qwen3-32B (CNY2/8)")),
    ("qwen3-30b-a3b",        _Price( 0.11,    0.43, "Qwen3-30B-A3B (CNY0.75/3)")),
    ("qwen3-14b",            _Price( 0.14,    0.58, "Qwen3-14B (CNY1/4)")),
    ("qwen3-8b",             _Price( 0.07,    0.29, "Qwen3-8B (CNY0.5/2)")),
    ("qwen3-4b",             _Price( 0.04,    0.17, "Qwen3-4B (CNY0.3/1.2)")),
    ("qwen3-1.7b",           _Price( 0.04,    0.17, "Qwen3-1.7B (CNY0.3/1.2)")),
    ("qwen3-0.6b",           _Price( 0.04,    0.17, "Qwen3-0.6B (CNY0.3/1.2)")),
    ("qwen3-next-80b-a3b-thinking", _Price(0.14, 1.45, "Qwen3-Next-80B-A3B-Thinking (CNY1/10)")),
    ("qwen3-next-80b-a3b-instruct", _Price(0.14, 0.58, "Qwen3-Next-80B-A3B-Instruct (CNY1/4)")),

    # ── 通用 Qwen（兜底）──
    ("qwen",               _Price( 0.12,    1.16, "Qwen (default, CNY0.8/8)")),
]


def lookup_price(model: str) -> _Price | None:
    """返回模型定价；未知模型返回 None。"""
    m = model.lower()
    for key, price in _PRICING:
        if key in m:
            return price
    return None


def estimate_cost(model: str, input_tokens: int, output_tokens: int) -> float | None:
    """返回预估费用（USD），未知模型返回 None。"""
    price = lookup_price(model)
    if price is None:
        return None
    return (price.input * input_tokens + price.output * output_tokens) / 1_000_000


# ════════════════════════════════════════════════════════════════════════
# Token 估算（用于 dry-run 预估）
# ════════════════════════════════════════════════════════════════════════

# 粗略估算：中文约 1.5 chars/token，英文约 4 chars/token
# 混合文本取均值 ~2.5 chars/token，保守估算用 2.0
_CHARS_PER_TOKEN = 2.0

# 每次请求的固定开销：system prompt + JSON schema 说明 + 输出格式提示
_SYSTEM_TOKEN_OVERHEAD = 120  # tokens
_OUTPUT_TOKEN_ESTIMATE = 200  # 每道题的平均输出（答案字母 + 解析约 150 字）
_OUTPUT_TOKEN_ESTIMATE_THINK = 1500  # 推理/思考模式：思维链 + 答案


def estimate_prompt_tokens(prompt_text: str) -> int:
    """根据 prompt 文本长度粗估 input token 数（保守估算）。"""
    return int(len(prompt_text) / _CHARS_PER_TOKEN) + _SYSTEM_TOKEN_OVERHEAD


def estimate_task_cost(
    model: str,
    tasks: list[dict],
    questions: list,
    enable_thinking: bool | None = None,
) -> dict:
    """对一批 tasks 进行 dry-run 费用预估。

    返回：
        {
          "total_tasks":      int,
          "est_input_tokens": int,
          "est_output_tokens":int,
          "est_total_tokens": int,
          "est_cost_usd":     float | None,
          "price_note":       str,
          "model":            str,
        }
    """
    from med_exam_toolkit.ai.client import is_reasoning_model, is_hybrid_thinking_model
    from med_exam_toolkit.ai.prompt import build_subquestion_prompt

    pure_r   = is_reasoning_model(model)
    hybrid   = is_hybrid_thinking_model(model)
    thinking = pure_r or (hybrid and enable_thinking)

    out_est = _OUTPUT_TOKEN_ESTIMATE_THINK if thinking else _OUTPUT_TOKEN_ESTIMATE

    total_input = 0
    for t in tasks:
        q  = questions[t["qi"]]
        sq = q.sub_questions[t["si"]]
        prompt = build_subquestion_prompt(
            q, sq,
            need_answer=t.get("need_answer", True),
            need_discuss=t.get("need_discuss", True),
        )
        total_input += estimate_prompt_tokens(prompt)

    total_output = len(tasks) * out_est
    cost         = estimate_cost(model, total_input, total_output)
    price        = lookup_price(model)

    return {
        "total_tasks":       len(tasks),
        "est_input_tokens":  total_input,
        "est_output_tokens": total_output,
        "est_total_tokens":  total_input + total_output,
        "est_cost_usd":      cost,
        "price_note":        price.note if price else "未知模型（无定价）",
        "model":             model,
    }


# ════════════════════════════════════════════════════════════════════════
# 运行时 Token 累加器
# ════════════════════════════════════════════════════════════════════════

@dataclass
class CostTracker:
    """线程安全的 token 用量累加器，附带费用换算。"""

    model: str
    _lock:             threading.Lock = field(default_factory=threading.Lock, repr=False)
    _prompt_tokens:    int = 0
    _completion_tokens: int = 0
    _requests:         int = 0
    _failed_requests:  int = 0

    def add_response(self, response: object) -> None:
        """从 API 响应对象中提取并累加 token 用量。"""
        try:
            usage = getattr(response, "usage", None)
            if usage is None:
                return
            pt = getattr(usage, "prompt_tokens",     0) or 0
            ct = getattr(usage, "completion_tokens", 0) or 0
            with self._lock:
                self._prompt_tokens     += pt
                self._completion_tokens += ct
                self._requests          += 1
        except Exception:
            pass

    def add_failure(self) -> None:
        with self._lock:
            self._failed_requests += 1

    def add(self, prompt_tokens: int, completion_tokens: int) -> None:
        """手动累加（测试用）。"""
        with self._lock:
            self._prompt_tokens     += prompt_tokens
            self._completion_tokens += completion_tokens
            self._requests          += 1

    # ── 只读属性 ──

    @property
    def prompt_tokens(self) -> int:
        return self._prompt_tokens

    @property
    def completion_tokens(self) -> int:
        return self._completion_tokens

    @property
    def total_tokens(self) -> int:
        return self._prompt_tokens + self._completion_tokens

    @property
    def requests(self) -> int:
        return self._requests

    @property
    def failed_requests(self) -> int:
        return self._failed_requests

    def cost_usd(self) -> float | None:
        return estimate_cost(self.model, self._prompt_tokens, self._completion_tokens)

    def summary(self, elapsed_sec: float = 0.0) -> str:
        """生成人类可读的费用统计摘要（多行字符串）。"""
        lines: list[str] = []
        price = lookup_price(self.model)

        lines.append(f"  📊 Token 用量统计")
        lines.append(f"  {'─'*50}")
        lines.append(f"  模型:        {self.model}")
        if price:
            lines.append(f"  定价参考:    {price.note}  "
                         f"(输入 ${price.input:.2f}/1M, 输出 ${price.output:.2f}/1M)")

        lines.append(f"  {'─'*50}")
        lines.append(f"  成功请求:    {self.requests} 次"
                     + (f"  失败: {self.failed_requests} 次" if self.failed_requests else ""))
        lines.append(f"  输入 tokens: {self._prompt_tokens:>10,}")
        lines.append(f"  输出 tokens: {self._completion_tokens:>10,}")
        lines.append(f"  合计 tokens: {self.total_tokens:>10,}")

        cost = self.cost_usd()
        if cost is not None:
            lines.append(f"  {'─'*50}")
            cny = cost * 7.2   # 粗略汇率，仅供参考
            lines.append(f"  预估费用:    ${cost:.4f} USD  ≈ ¥{cny:.2f} CNY")
            if self.requests > 0:
                per_req = cost / self.requests
                lines.append(f"  单题均价:    ${per_req:.5f} USD")
        else:
            lines.append(f"  费用:        未知定价模型，无法估算")

        if elapsed_sec > 0 and self.total_tokens > 0:
            tps = self.total_tokens / elapsed_sec
            lines.append(f"  吞吐速率:    {tps:,.0f} tokens/s")

        return "\n".join(lines)