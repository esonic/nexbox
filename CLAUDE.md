# NexBox

AI 协作平台:用户通过 Web UI 驱动多种编码 Agent(Claude / Codex)在隔离工作区中执行任务,支持多会话、工作流编排、文件编辑、Git diff、终端、MCP 工具与 Marketplace。

## 技术栈

**前端 (`web/`)**
- React 19 + TypeScript + Vite 7
- Tailwind CSS + Radix UI(`shadcn/ui` 风格组件)
- React Router 7、Zustand(状态)、TanStack Query(数据)
- Monaco Editor(代码编辑)、xterm.js(终端)、ReactFlow + dagre(工作流可视化)
- react-markdown + rehype/remark + highlight.js(消息渲染)

**后端 (`server/`)**
- Go,单二进制,内嵌前端静态资源(`webdist`)
- HTTP REST API + WebSocket(`/ws`,实时推送 agent 事件)
- SQLite 持久化(`store/`)
- 通过 CLI 子进程对接 Claude / Codex agent
- 内置 MCP server 与 Chrome DevTools MCP 集成

**辅助**
- `build.sh`:构建脚本

## 核心功能

- 多 Agent 会话:并行运行 Claude / Codex,流式输出消息、思考、工具调用
- 文件浏览/编辑:Monaco 编辑器、文件 diff 查看与提交
- 终端:每个会话可附加交互式 shell
- 工作流(Workflow):DAG 编排多个 agent 步骤
- 项目 / Marketplace:管理项目与可复用的 MCP、技能、插件
- 调度器:定时触发会话或工作流
- Web 分享:将 HTML 产物对外公开访问

## 目录结构

```
nexbox/
├── web/                     # 前端 SPA
│   ├── src/
│   │   ├── App.tsx          # 路由入口
│   │   ├── pages/           # 顶层页面(SessionWorkspace、Projects、Workflows、Marketplace、Login...)
│   │   ├── components/      # chat / files / diff / workflow / layout / ui
│   │   ├── stores/          # Zustand stores(app、layout、fileBrowser)
│   │   ├── hooks/           # useChatSession、useIsMobile 等
│   │   └── lib/             # api、ws、工具
│   └── vite.config.ts       # /api、/ws 代理到 :9040
│
├── server/                  # Go 后端
│   ├── main.go              # 入口、CORS、路由装配
│   ├── api/                 # HTTP handlers(chat / files / git / workflows / marketplace / scheduler...)
│   ├── agent/               # Claude / Codex agent 适配与会话
│   ├── workflow/            # DAG 引擎与执行器
│   ├── store/               # SQLite 数据层
│   ├── mcp/                 # 内置 MCP server + Chrome 集成
│   ├── ws/                  # WebSocket hub
│   ├── tools/、config/      # 工具与配置
│   └── webdist/             # 内嵌前端构建产物
│
├── build.sh
```

