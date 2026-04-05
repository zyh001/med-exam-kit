"""
ehafo API 抓包脚本
用法：mitmweb -s ehafo_capture.py
      mitmdump -s ehafo_capture.py

实时保存所有请求到 ehafo_apis.json
运行结束后生成 ehafo_report.md 报告
"""

import json
import os
import re
import time
from collections import defaultdict
from datetime import datetime
from mitmproxy import http, ctx

# ── 配置区 ───────────────────────────────────────────────────
TARGET_HOSTS = [
    "ehafo.com",
    "yihafo.com",
    "yiqizuoti.com",
]
# 留空则捕获所有 host（调试用）；填入后只捕获目标域名
FILTER_HOSTS = TARGET_HOSTS

OUTPUT_JSON = "ehafo_apis.json"
OUTPUT_MD   = "ehafo_report.md"

# 敏感字段（在日志里打码）
SENSITIVE_KEYS = {
    "password", "passwd", "pwd", "token", "access_token", "refresh_token",
    "secret", "sign", "signature", "id_card", "idcard", "bank_card",
    "card_no", "cvv", "phone", "mobile", "auth_code",
}
# ────────────────────────────────────────────────────────────


def mask_sensitive(obj, depth=0):
    """递归对敏感字段打码"""
    if depth > 8:
        return obj
    if isinstance(obj, dict):
        return {
            k: "***" if k.lower() in SENSITIVE_KEYS else mask_sensitive(v, depth + 1)
            for k, v in obj.items()
        }
    if isinstance(obj, list):
        return [mask_sensitive(i, depth + 1) for i in obj]
    return obj


def try_parse_body(content: bytes, content_type: str) -> tuple[str, any]:
    """尝试解析请求/响应体，返回 (格式, 解析结果)"""
    if not content:
        return "empty", None
    ct = (content_type or "").lower()
    try:
        if "json" in ct or content.lstrip().startswith(b"{") or content.lstrip().startswith(b"["):
            return "json", json.loads(content)
    except Exception:
        pass
    try:
        if "form" in ct:
            from urllib.parse import parse_qs
            parsed = parse_qs(content.decode("utf-8", errors="replace"))
            return "form", {k: v[0] if len(v) == 1 else v for k, v in parsed.items()}
    except Exception:
        pass
    try:
        text = content.decode("utf-8", errors="replace")
        if len(text) < 2000:
            return "text", text
        return "text", text[:2000] + f"... [截断, 总长 {len(content)} 字节]"
    except Exception:
        pass
    return "binary", f"[二进制, {len(content)} 字节]"


class EhafoCapture:
    def __init__(self):
        self.records: list[dict] = []
        self.stats = defaultdict(lambda: {"count": 0, "methods": set(), "status_codes": set()})
        self.start_time = datetime.now()
        self._load_existing()

    def _load_existing(self):
        """加载上次的抓包结果，支持增量追加"""
        if os.path.exists(OUTPUT_JSON):
            try:
                with open(OUTPUT_JSON, "r", encoding="utf-8") as f:
                    self.records = json.load(f)
                ctx.log.info(f"[ehafo] 加载已有记录 {len(self.records)} 条")
            except Exception:
                self.records = []

    def _should_capture(self, host: str) -> bool:
        if not FILTER_HOSTS:
            return True
        return any(host == h or host.endswith("." + h) for h in FILTER_HOSTS)

    def request(self, flow: http.HTTPFlow):
        """请求发出时打印简要信息"""
        host = flow.request.pretty_host
        if not self._should_capture(host):
            return
        ctx.log.info(f"[→] {flow.request.method:6s} {flow.request.pretty_url}")

    def response(self, flow: http.HTTPFlow):
        """响应返回时完整记录"""
        host = flow.request.pretty_host
        if not self._should_capture(host):
            return

        req = flow.request
        res = flow.response

        # 解析请求体
        req_ct = req.headers.get("content-type", "")
        req_fmt, req_body = try_parse_body(req.content, req_ct)
        if isinstance(req_body, (dict, list)):
            req_body = mask_sensitive(req_body)

        # 解析响应体
        res_ct = res.headers.get("content-type", "")
        res_fmt, res_body = try_parse_body(res.content, res_ct)
        if isinstance(res_body, (dict, list)):
            res_body = mask_sensitive(res_body)

        # 整理请求头（过滤掉冗余的标准头）
        skip_req_headers = {"accept-encoding", "connection", "host"}
        req_headers = {
            k: v for k, v in req.headers.items()
            if k.lower() not in skip_req_headers
        }

        record = {
            "id":          len(self.records) + 1,
            "timestamp":   datetime.now().isoformat(timespec="seconds"),
            "method":      req.method,
            "host":        host,
            "path":        req.path,
            "url":         req.pretty_url,
            "req_headers": req_headers,
            "req_format":  req_fmt,
            "req_body":    req_body,
            "status":      res.status_code,
            "res_headers": dict(res.headers),
            "res_format":  res_fmt,
            "res_body":    res_body,
            "latency_ms":  round((res.timestamp_end - req.timestamp_start) * 1000)
                           if res.timestamp_end and req.timestamp_start else None,
        }

        self.records.append(record)

        # 更新统计
        path_key = re.sub(r"/\d+", "/{id}", req.path)  # 归一化路径里的数字 ID
        path_key = re.sub(r"[?#].*", "", path_key)
        stat_key = f"{host}{path_key}"
        self.stats[stat_key]["count"] += 1
        self.stats[stat_key]["methods"].add(req.method)
        self.stats[stat_key]["status_codes"].add(res.status_code)
        self.stats[stat_key]["host"] = host
        self.stats[stat_key]["path"] = path_key

        # 实时保存 JSON
        self._save_json()

        # 控制台输出
        status_icon = "✓" if res.status_code < 400 else "✗"
        ctx.log.info(
            f"[{status_icon}] {req.method:6s} {res.status_code} "
            f"{req.pretty_url}  ({record['latency_ms']}ms)"
        )

    def _save_json(self):
        with open(OUTPUT_JSON, "w", encoding="utf-8") as f:
            json.dump(self.records, f, ensure_ascii=False, indent=2)

    def done(self):
        """mitmproxy 退出时生成 Markdown 报告"""
        self._save_json()
        self._generate_report()
        ctx.log.info(f"[ehafo] 已保存 {len(self.records)} 条记录 → {OUTPUT_JSON}")
        ctx.log.info(f"[ehafo] 报告已生成 → {OUTPUT_MD}")

    def _generate_report(self):
        now = datetime.now()
        duration = now - self.start_time

        # 按 host 分组
        by_host: dict[str, list[dict]] = defaultdict(list)
        for r in self.records:
            by_host[r["host"]].append(r)

        # 收集所有出现的 Token/认证头（打码）
        auth_headers_seen = set()
        for r in self.records:
            for k in r.get("req_headers", {}):
                if k.lower() in {"authorization", "token", "x-token",
                                  "x-auth-token", "x-access-token"}:
                    auth_headers_seen.add(k)

        lines = [
            f"# ehafo API 抓包报告",
            f"",
            f"生成时间：{now.strftime('%Y-%m-%d %H:%M:%S')}  ",
            f"抓包时长：{str(duration).split('.')[0]}  ",
            f"总请求数：{len(self.records)}  ",
            f"涉及域名：{len(by_host)}  ",
            f"认证头字段：{', '.join(sorted(auth_headers_seen)) or '未检测到'}",
            f"",
            f"---",
            f"",
            f"## 接口汇总",
            f"",
            f"| # | 域名 | 路径 | 方法 | 调用次数 | 状态码 |",
            f"|---|------|------|------|----------|--------|",
        ]

        for i, (key, stat) in enumerate(
            sorted(self.stats.items(), key=lambda x: -x[1]["count"]), 1
        ):
            methods = "/".join(sorted(stat["methods"]))
            codes   = "/".join(str(c) for c in sorted(stat["status_codes"]))
            lines.append(
                f"| {i} | `{stat['host']}` | `{stat['path']}` "
                f"| {methods} | {stat['count']} | {codes} |"
            )

        lines += ["", "---", ""]

        # 按 host 展开详情
        for host, records in sorted(by_host.items()):
            lines += [f"## {host}", f"", f"共 {len(records)} 个请求", ""]

            # 按路径去重展示
            seen_paths: dict[str, dict] = {}
            for r in records:
                norm_path = re.sub(r"/\d+", "/{id}", r["path"].split("?")[0])
                if norm_path not in seen_paths:
                    seen_paths[norm_path] = r

            for norm_path, r in sorted(seen_paths.items()):
                lines += [
                    f"### `{r['method']} {norm_path}`",
                    f"",
                    f"**完整 URL 示例：** `{r['url']}`  ",
                    f"**状态码：** {r['status']}  ",
                    f"**延迟：** {r['latency_ms']}ms  ",
                    f"",
                ]

                # 请求头（只列非标准头）
                interesting_headers = {
                    k: v for k, v in r["req_headers"].items()
                    if k.lower() not in {
                        "accept", "accept-language", "user-agent",
                        "content-length", "cache-control"
                    }
                }
                if interesting_headers:
                    lines.append("**请求头：**")
                    lines.append("```")
                    for k, v in interesting_headers.items():
                        display_v = "***" if k.lower() in SENSITIVE_KEYS else v
                        lines.append(f"{k}: {display_v}")
                    lines.append("```")
                    lines.append("")

                # 请求体
                if r["req_body"] is not None and r["req_format"] != "empty":
                    lines.append(f"**请求体（{r['req_format']}）：**")
                    lines.append("```json" if r["req_format"] == "json" else "```")
                    body_str = (
                        json.dumps(r["req_body"], ensure_ascii=False, indent=2)
                        if isinstance(r["req_body"], (dict, list))
                        else str(r["req_body"])
                    )
                    lines.append(body_str[:1500])
                    if len(body_str) > 1500:
                        lines.append("... [截断]")
                    lines.append("```")
                    lines.append("")

                # 响应体
                if r["res_body"] is not None and r["res_format"] != "empty":
                    lines.append(f"**响应体（{r['res_format']}）：**")
                    lines.append("```json" if r["res_format"] == "json" else "```")
                    body_str = (
                        json.dumps(r["res_body"], ensure_ascii=False, indent=2)
                        if isinstance(r["res_body"], (dict, list))
                        else str(r["res_body"])
                    )
                    lines.append(body_str[:2000])
                    if len(body_str) > 2000:
                        lines.append("... [截断]")
                    lines.append("```")
                    lines.append("")

                lines.append("---")
                lines.append("")

        with open(OUTPUT_MD, "w", encoding="utf-8") as f:
            f.write("\n".join(lines))


# mitmproxy addon 入口
addon = EhafoCapture()

def request(flow: http.HTTPFlow):
    addon.request(flow)

def response(flow: http.HTTPFlow):
    addon.response(flow)

def done():
    addon.done()
