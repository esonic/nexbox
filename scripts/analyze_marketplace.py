#!/usr/bin/env python3
"""
Marketplace Plugin System Analyzer
系统性分析 stable/marketplace 目录下所有插件的配置内容
"""

import json
import os
import re
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

MARKETPLACE_ROOT = Path(__file__).parent.parent / "stable" / "marketplace"


def read_json(path: Path) -> dict | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception as e:
        print(f"  [!] Failed to read {path}: {e}", file=sys.stderr)
        return None


def detect_auth_type(mcp_config: dict) -> tuple[str, list[str]]:
    """
    从 .mcp.json 推断 auth_type 和 env_vars
    Returns: (auth_type, env_vars)
      - "none"             : 无鉴权 (stdio 或无 auth 的 HTTP)
      - "apikey"           : 需要 API Key (headers 中有 ${VAR})
      - "oauth_discovery"  : MCP 标准 OAuth Discovery (HTTP URL 无 auth 字段)
      - "oauth"            : 显式 oauth 字段 (如 Slack)
      - "sse_discovery"    : SSE transport + OAuth Discovery (如 Asana)
    """
    # 找到第一个 server 配置（支持两种格式：顶层直接是 server 或 mcpServers 包裹）
    servers = {}
    if "mcpServers" in mcp_config:
        servers = mcp_config["mcpServers"]
    else:
        # 每个 top-level key 可能就是 server 名
        for k, v in mcp_config.items():
            if isinstance(v, dict):
                servers[k] = v

    env_vars = []
    auth_types = set()

    for name, config in servers.items():
        transport_type = config.get("type", "stdio")
        command = config.get("command")
        url = config.get("url")
        headers = config.get("headers", {})
        oauth = config.get("oauth")

        if oauth:
            auth_types.add("oauth")
            continue

        if command:
            # stdio transport
            # 检查 env 中是否有需要填写的变量
            env = config.get("env", {})
            for val in env.values():
                if isinstance(val, str) and val.startswith("${"):
                    m = re.findall(r"\$\{(\w+)\}", val)
                    env_vars.extend(m)
            if env_vars:
                auth_types.add("apikey")
            else:
                auth_types.add("none_stdio")
            continue

        if url:
            # HTTP/SSE transport
            has_auth_header = False
            for h_name, h_val in headers.items():
                if "authorization" in h_name.lower() or "api-key" in h_name.lower():
                    m = re.findall(r"\$\{(\w+)\}", str(h_val))
                    if m:
                        env_vars.extend(m)
                        has_auth_header = True

            if has_auth_header:
                auth_types.add("apikey")
            elif transport_type == "sse":
                auth_types.add("sse_discovery")
            else:
                auth_types.add("oauth_discovery")

    if not auth_types:
        return "none", env_vars

    # 优先级: oauth > apikey > sse_discovery > oauth_discovery > none_stdio
    priority = ["oauth", "apikey", "sse_discovery", "oauth_discovery", "none_stdio", "none"]
    for p in priority:
        if p in auth_types:
            # normalize none_stdio -> none
            return ("none" if p == "none_stdio" else p), list(set(env_vars))

    return "none", env_vars


def detect_plugin_components(plugin_dir: Path) -> list[str]:
    """检测插件目录中包含哪些组件"""
    components = []
    if (plugin_dir / ".mcp.json").exists():
        components.append("mcp")
    if (plugin_dir / "skills").exists():
        skills = list((plugin_dir / "skills").rglob("SKILL.md"))
        if skills:
            components.append("skills")
    if (plugin_dir / "commands").exists():
        cmds = list((plugin_dir / "commands").glob("*.md"))
        if cmds:
            components.append("commands")
    if (plugin_dir / "agents").exists():
        agents = list((plugin_dir / "agents").glob("*.md"))
        if agents:
            components.append("agents")
    if (plugin_dir / "hooks").exists():
        if (plugin_dir / "hooks" / "hooks.json").exists():
            components.append("hooks")
    return components


def analyze_mcp_json(mcp_path: Path) -> dict:
    """分析单个 .mcp.json 文件"""
    raw = read_json(mcp_path)
    if not raw:
        return {}

    auth_type, env_vars = detect_auth_type(raw)

    # 提取所有 server 配置
    servers = {}
    if "mcpServers" in raw:
        servers = raw["mcpServers"]
    else:
        for k, v in raw.items():
            if isinstance(v, dict):
                servers[k] = v

    result = {
        "path": str(mcp_path.relative_to(MARKETPLACE_ROOT)),
        "auth_type": auth_type,
        "env_vars": env_vars,
        "raw": raw,
        "servers": {},
    }

    for name, config in servers.items():
        transport = config.get("type", "stdio")
        result["servers"][name] = {
            "transport": transport,
            "command": config.get("command"),
            "args": config.get("args", []),
            "url": config.get("url"),
            "has_oauth_field": "oauth" in config,
            "has_headers": bool(config.get("headers")),
        }

    return result


def analyze_plugin_dir(plugin_dir: Path, source_name: str = "") -> dict:
    """分析一个 plugin 目录"""
    info = {
        "name": source_name or plugin_dir.name,
        "dir": str(plugin_dir.relative_to(MARKETPLACE_ROOT)),
        "components": detect_plugin_components(plugin_dir),
        "plugin_json": None,
        "mcp": None,
        "skills": [],
        "commands": [],
        "agents": [],
        "hooks": [],
    }

    # plugin.json
    pj = plugin_dir / ".claude-plugin" / "plugin.json"
    if pj.exists():
        data = read_json(pj)
        if data:
            info["plugin_json"] = {
                "name": data.get("name"),
                "description": data.get("description", "")[:80],
                "author": data.get("author", {}).get("name", ""),
            }

    # .mcp.json
    mcp = plugin_dir / ".mcp.json"
    if mcp.exists():
        info["mcp"] = analyze_mcp_json(mcp)

    # skills
    if (plugin_dir / "skills").exists():
        for skill_md in sorted((plugin_dir / "skills").rglob("SKILL.md")):
            info["skills"].append(str(skill_md.relative_to(plugin_dir)))

    # commands
    if (plugin_dir / "commands").exists():
        for cmd_md in sorted((plugin_dir / "commands").glob("*.md")):
            info["commands"].append(cmd_md.name)

    # agents
    if (plugin_dir / "agents").exists():
        for agent_md in sorted((plugin_dir / "agents").glob("*.md")):
            info["agents"].append(agent_md.name)

    # hooks
    if (plugin_dir / "hooks" / "hooks.json").exists():
        hooks_data = read_json(plugin_dir / "hooks" / "hooks.json")
        if hooks_data:
            info["hooks"] = list(hooks_data.keys()) if isinstance(hooks_data, dict) else ["defined"]

    return info


def analyze_marketplace_json(mj_path: Path) -> list[dict]:
    """分析 marketplace.json 文件，返回所有插件条目"""
    data = read_json(mj_path)
    if not data:
        return []

    repo_dir = mj_path.parent.parent  # .claude-plugin 的上一级就是仓库根
    plugins = []

    for entry in data.get("plugins", []):
        name = entry.get("name", "")
        description = entry.get("description", "")[:80]
        source = entry.get("source", "")
        category = entry.get("category", "")
        homepage = entry.get("homepage", "")
        lsp_servers = entry.get("lspServers", {})
        tags = entry.get("tags", [])
        skills_list = entry.get("skills", [])  # skills repo 的格式

        plugin_info = {
            "name": name,
            "description": description,
            "category": category,
            "homepage": homepage,
            "tags": tags,
            "source_raw": source,
            "source_type": "unknown",
            "type": "plugin",  # 默认
            "auth_type": "none",
            "env_vars": [],
            "components": [],
            "skills_paths": skills_list,
        }

        # 判断 source 类型
        if lsp_servers:
            plugin_info["type"] = "lsp"
            plugin_info["source_type"] = "lsp_inline"
            plugin_info["lsp_servers"] = list(lsp_servers.keys())
            plugins.append(plugin_info)
            continue

        if not source:
            plugins.append(plugin_info)
            continue

        # skills repo 格式：skills 字段指定技能路径列表
        if skills_list and source == "./":
            plugin_info["type"] = "skill"
            plugin_info["source_type"] = "skills_collection"
            skill_names = []
            for sp in skills_list:
                sp_path = repo_dir / sp.lstrip("./")
                skill_md = sp_path / "SKILL.md"
                if skill_md.exists():
                    skill_names.append(sp.split("/")[-1])
            plugin_info["skills_names"] = skill_names
            plugin_info["components"] = ["skills"]
            plugins.append(plugin_info)
            continue

        if isinstance(source, dict):
            # Remote git URL
            plugin_info["source_type"] = "remote_git"
            plugin_info["remote_url"] = source.get("url", "")
            plugins.append(plugin_info)
            continue

        if isinstance(source, str) and source.startswith("./"):
            # Local path
            source_path = repo_dir / source.lstrip("./")
            if source_path.is_dir():
                plugin_info["source_type"] = "local_dir"
                # 深入分析子目录
                sub_info = analyze_plugin_dir(source_path, name)
                plugin_info["components"] = sub_info["components"]
                plugin_info["commands"] = sub_info.get("commands", [])
                plugin_info["agents"] = sub_info.get("agents", [])
                plugin_info["hooks"] = sub_info.get("hooks", [])
                plugin_info["skills_list"] = sub_info.get("skills", [])

                if sub_info["mcp"]:
                    plugin_info["type"] = "mcp"
                    plugin_info["auth_type"] = sub_info["mcp"]["auth_type"]
                    plugin_info["env_vars"] = sub_info["mcp"]["env_vars"]
                    plugin_info["mcp_servers"] = sub_info["mcp"]["servers"]
                elif "skills" in sub_info["components"]:
                    plugin_info["type"] = "skill"
                else:
                    plugin_info["type"] = "plugin"
            elif source_path.suffix == ".md":
                # 直接指向 .md 文件（如 trading-ideas.md）
                plugin_info["source_type"] = "command_file"
                plugin_info["type"] = "plugin"
                plugin_info["components"] = ["commands"]
                plugin_info["commands"] = [source_path.name]
            plugins.append(plugin_info)
            continue

        plugins.append(plugin_info)

    return plugins


def main():
    print("=" * 80)
    print("MARKETPLACE PLUGIN SYSTEM ANALYZER")
    print(f"Root: {MARKETPLACE_ROOT}")
    print("=" * 80)

    all_plugins: list[dict] = []

    # 1. 扫描所有 marketplace.json
    print("\n### Phase 1: Scanning marketplace.json files ###\n")
    for mj in sorted(MARKETPLACE_ROOT.rglob(".claude-plugin/marketplace.json")):
        rel = mj.relative_to(MARKETPLACE_ROOT)
        print(f"[marketplace.json] {rel}")
        plugins = analyze_marketplace_json(mj)
        print(f"  Found {len(plugins)} plugins")
        all_plugins.extend(plugins)

    # 2. 按 type 分组汇总
    print("\n" + "=" * 80)
    print("### Phase 2: Summary by Type ###")
    print("=" * 80)

    by_type: dict[str, list[dict]] = defaultdict(list)
    by_auth: dict[str, list[dict]] = defaultdict(list)

    for p in all_plugins:
        by_type[p["type"]].append(p)
        by_auth[p["auth_type"]].append(p)

    print(f"\nTotal plugins analyzed: {len(all_plugins)}")
    print("\nBy Type:")
    for t, plist in sorted(by_type.items()):
        print(f"  {t:20s}: {len(plist):3d}")

    print("\nBy Auth Type (MCP servers only):")
    for a, plist in sorted(by_auth.items()):
        if a != "none" or any(p["type"] == "mcp" for p in plist):
            print(f"  {a:20s}: {len(plist):3d}")

    # 3. 详细列出每种类型
    print("\n" + "=" * 80)
    print("### Phase 3: Detailed Plugin List ###")
    print("=" * 80)

    # --- MCP Plugins ---
    print("\n--- MCP Connectors ---")
    print(f"{'Name':<25} {'Auth':<18} {'Transport':<8} {'Env Vars'}")
    print("-" * 80)
    for p in sorted(by_type["mcp"], key=lambda x: x["name"]):
        servers = p.get("mcp_servers", {})
        transport = ", ".join(v.get("transport", "stdio") for v in servers.values())
        env = ", ".join(p.get("env_vars", [])) or "-"
        print(f"  {p['name']:<23} {p['auth_type']:<18} {transport:<8} {env}")

    # --- Plugin type ---
    print("\n--- Plugins (Commands/Agents/Hooks) ---")
    print(f"{'Name':<30} {'Components'}")
    print("-" * 70)
    for p in sorted(by_type["plugin"], key=lambda x: x["name"]):
        comps = ", ".join(p.get("components", [])) or "(none detected)"
        cmds = p.get("commands", [])
        agents = p.get("agents", [])
        extra = ""
        if cmds:
            extra += f" cmds={cmds}"
        if agents:
            extra += f" agents={agents}"
        print(f"  {p['name']:<28} {comps}{extra}")

    # --- Skill type ---
    print("\n--- Skills ---")
    print(f"{'Name':<30} {'Skills Count/List'}")
    print("-" * 70)
    for p in sorted(by_type["skill"], key=lambda x: x["name"]):
        sk = p.get("skills_names") or p.get("skills_list", [])
        sk_str = f"{len(sk)} skills: {', '.join(sk[:5])}" if sk else "(skills dir)"
        print(f"  {p['name']:<28} {sk_str}")

    # --- LSP type ---
    print("\n--- LSP Servers ---")
    print(f"{'Name':<30} {'Language Servers'}")
    print("-" * 70)
    for p in sorted(by_type["lsp"], key=lambda x: x["name"]):
        lsps = ", ".join(p.get("lsp_servers", []))
        print(f"  {p['name']:<28} {lsps}")

    # 4. Auth Type Deep Dive
    print("\n" + "=" * 80)
    print("### Phase 4: Auth Type Analysis ###")
    print("=" * 80)

    auth_groups = {
        "none": "无鉴权 (stdio or no-auth HTTP)",
        "apikey": "API Key (Bearer token in headers or env)",
        "oauth_discovery": "MCP Protocol OAuth Discovery (HTTP, 无 auth 字段)",
        "sse_discovery": "SSE Transport + OAuth Discovery",
        "oauth": "显式 OAuth (clientId in config)",
    }

    for auth_type, desc in auth_groups.items():
        matching = [p for p in all_plugins if p["auth_type"] == auth_type]
        if not matching:
            continue
        print(f"\n[{auth_type}] {desc}")
        print(f"  Count: {len(matching)}")
        names = [p["name"] for p in matching]
        print(f"  Plugins: {', '.join(names)}")
        # 详细 env vars
        for p in matching:
            if p.get("env_vars"):
                print(f"    {p['name']}: env_vars = {p['env_vars']}")
            if p.get("mcp_servers"):
                for sname, sc in p["mcp_servers"].items():
                    url = sc.get("url") or (sc.get("command") + " " + " ".join(sc.get("args", [])))
                    print(f"    {p['name']}/{sname}: transport={sc['transport']}, url/cmd={url}")

    # 5. Component Analysis
    print("\n" + "=" * 80)
    print("### Phase 5: Component Capability Analysis ###")
    print("=" * 80)

    component_counts: dict[str, int] = defaultdict(int)
    multi_component = []

    for p in all_plugins:
        comps = p.get("components", [])
        for c in comps:
            component_counts[c] += 1
        if len(comps) > 1:
            multi_component.append(p)

    print("\nComponent counts across all plugins:")
    for c, count in sorted(component_counts.items(), key=lambda x: -x[1]):
        print(f"  {c:<15}: {count}")

    print(f"\nPlugins with MULTIPLE components ({len(multi_component)}):")
    for p in multi_component:
        print(f"  {p['name']:<25}: {' + '.join(sorted(p['components']))}")

    # 6. Transport & URL analysis
    print("\n" + "=" * 80)
    print("### Phase 6: MCP Transport Details ###")
    print("=" * 80)

    stdio_plugins = []
    http_plugins = []
    sse_plugins = []

    for p in all_plugins:
        for sname, sc in p.get("mcp_servers", {}).items():
            t = sc.get("transport", "stdio")
            cmd = sc.get("command")
            url = sc.get("url", "")
            entry = {"plugin": p["name"], "server": sname, "transport": t, "cmd": cmd, "url": url}
            if t == "stdio" or cmd:
                stdio_plugins.append(entry)
            elif t == "sse":
                sse_plugins.append(entry)
            else:
                http_plugins.append(entry)

    print(f"\nStdio (subprocess) MCP servers ({len(stdio_plugins)}):")
    for e in stdio_plugins:
        cmd_str = e.get("cmd", "") + " " + " ".join(e.get("args", []) if e.get("args") else [])
        print(f"  {e['plugin']:<20}: {cmd_str.strip()[:60]}")

    print(f"\nHTTP MCP servers ({len(http_plugins)}):")
    for e in http_plugins:
        print(f"  {e['plugin']:<20}: {e.get('url', '')[:60]}")

    print(f"\nSSE MCP servers ({len(sse_plugins)}):")
    for e in sse_plugins:
        print(f"  {e['plugin']:<20}: {e.get('url', '')[:60]}")

    # 7. Summary Table (markdown-friendly)
    print("\n" + "=" * 80)
    print("### Phase 7: Summary Table (Markdown) ###")
    print("=" * 80)
    print()
    print("| Plugin | Type | Auth | Transport | Components | Notes |")
    print("|--------|------|------|-----------|------------|-------|")
    for p in sorted(all_plugins, key=lambda x: (x["type"], x["name"])):
        t = p["type"]
        auth = p["auth_type"] if t == "mcp" else "-"
        servers = p.get("mcp_servers", {})
        transport = "/".join(set(v.get("transport", "stdio") for v in servers.values())) or "-"
        if any(v.get("command") for v in servers.values()):
            transport = "stdio"
        comps = ", ".join(sorted(set(p.get("components", [])))) or "-"
        notes = ""
        if p.get("env_vars"):
            notes = f"env: {', '.join(p['env_vars'])}"
        elif p.get("remote_url"):
            notes = f"remote git"
        print(f"| {p['name']:<25} | {t:<6} | {auth:<16} | {transport:<9} | {comps:<20} | {notes} |")

    print("\n[Done]")


if __name__ == "__main__":
    main()
