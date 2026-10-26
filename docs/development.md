# 开发指南

## 准备环境

依赖版本见[运行要求](../README.md#运行要求)。直接构建完整程序：

```bash
./build.sh
```

构建脚本包含 `npm ci`。只开发前端时，也可以手动执行 `npm ci --prefix web`；Go 模块由 `go run`、`go build` 和 `go test` 自动下载。

## 开发服务

先按[快速开始](../README.md#快速开始)完成构建。开发时使用独立的数据目录，新目录默认免登录；测试账号功能时，账号命令和服务必须指向同一目录：

```bash
export NEXBOX_DATA_DIR="$HOME/nexbox-dev-data"
./build.sh
```

终端一启动 Go 服务：

```bash
cd server
NEXBOX_DATA_DIR="$HOME/nexbox-dev-data" go run .
```

终端二启动前端：

```bash
cd web
npm run dev
```

前端默认地址为 `http://localhost:5173`，Vite 将 `/api` 与 `/ws` 转发到 `localhost:9040`。修改后端端口时，在运行前端的终端同步设置 `PORT`：

```bash
PORT=9050 npm run dev
```

`PORT` 只控制开发代理目标；Go 服务使用 `--port`。实际前端端口以 Vite 输出为准。

## 构建

```bash
./build.sh
./dist/nexbox --version
```

构建先生成前端，再更新 `server/webdist/`，最后通过 Go embed 生成单个可执行文件。`server/webdist/.gitkeep` 保证干净检出时 Go 包也能编译；其余前端构建产物不提交。

可覆盖产物路径：

```bash
NEXBOX_OUTPUT=dist/nexbox-preview ./build.sh
./dist/nexbox-preview
```

相对产物路径始终相对仓库根目录解析。默认版本来自 `git describe --tags --always --dirty`；源码包没有 Git 信息时使用 `dev`。

## 验证

```bash
cd server
go test ./...
```

默认服务端测试不启动宿主机浏览器，因此适合本地开发和普通 CI。真实 Chrome/Chromium 端到端测试需要安装浏览器并显式启用：

```bash
cd server
NEXBOX_CHROME_E2E=1 go test -v -count=1 -timeout=10m ./mcp/chrome
```

仓库的 **Browser E2E** workflow 也可从 GitHub Actions 页面手动运行。依赖已登录 CLI 或真实模型的测试仍按各测试文件中的环境变量显式启用；普通测试不要求配置模型服务。

```bash
cd web
npm test
npm run build
npm run lint
```

`npm test` 覆盖免登录路由、账号界面、会话排序、Git 刷新及市场安装作用域。仓库仍有历史 ESLint 问题；提交改动前检查受影响文件，并避免引入新的告警。

原生 MCP 联调使用临时配置和本机模型／MCP 测试服务。需要对应 CLI，但不调用真实模型：

```bash
cd server
MCP_NATIVE_INTEGRATION=1 go test ./api -run '^TestNative(Codex|Pi|Claude|Kimi)' -v
```

## 代码结构

| 路径 | 职责 |
| --- | --- |
| `web/src/components/` | 对话、文件、Git、终端和通用界面 |
| `web/src/hooks/`、`web/src/stores/` | 前端状态和交互协调 |
| `server/api/` | HTTP / WebSocket、项目和会话调度 |
| `server/agent/` | 各 Agent 的机器协议适配 |
| `server/store/` | SQLite 与本地数据模型 |
| `server/gitrepo/` | 仓库发现、范围解析和 Git 工作区 |
| `server/mcp/` | 内置 MCP 与浏览器工具 |
| `server/workflow/` | 工作流图与执行引擎 |

新增能力时保持 Agent 差异位于适配器内部；界面通过能力声明判断 fork、插话、模型切换和浏览器支持，不根据显示名称猜测行为。
