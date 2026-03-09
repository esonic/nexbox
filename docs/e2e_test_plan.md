# BoxNex E2E 测试计划

目标服务器：http://localhost:9040
测试方式：Chrome MCP 控制浏览器 + curl，端到端验证

> 注意：注册流程已改为 **邮箱 + 验证码 + 密码**，不再使用 username 字段。

---

## Group A — 注册 / 登录 / JWT

| ID | 操作 | 预期结果 |
|----|------|---------|
| A1 | POST /api/auth/send-verification {email:"admin@test.dev"} | 200 {ok:true}，验证码写入 logs/boxnex.log |
| A2 | POST /api/auth/register {email, password:"password123", code} | 201，返回 token（第一个用户自动成为 admin） |
| A3 | POST /api/auth/register 同邮箱再注册 | 409 email already registered |
| A4 | POST /api/auth/register {email, password:"short", code} 密码过短 | 400 |
| A5 | POST /api/auth/login {email:"admin@test.dev", password:"password123"} | 200，返回 token |
| A6 | GET /api/auth/me，带 A5 token | 200，email=admin@test.dev，role=admin |
| A7 | GET /api/auth/me，无 token | 401 |
| A8 | GET /api/info（无 token） | 200，mode="needs_login" |

## Group B — 余额 / 计费

| ID | 操作 | 预期结果 |
|----|------|---------|
| B1 | GET /api/balance，用 admin token | 200，balance_tokens>=0 |
| B2 | POST /api/admin/recharge {user_id:adminId, tokens:10000}（admin） | 200，ok:true |
| B3 | GET /api/balance 再查 | 200，balance_tokens==10000 |

## Group C — Projects CRUD + 隔离

| ID | 操作 | 预期结果 |
|----|------|---------|
| C1 | 注册 user2@test.dev（发验证码→注册） | 201 |
| C2 | admin 创建 project "proj-alpha" | 201，返回 id |
| C3 | admin GET /api/projects，能看到 proj-alpha | 200 |
| C4 | user2 GET /api/projects/{proj-alpha id} | 403 |
| C5 | user2 创建 project "proj-beta" | 201 |
| C6 | admin GET /api/projects，能看到两个 project | 200，len>=2 |

## Group D — Sessions CRUD + 隔离

| ID | 操作 | 预期结果 |
|----|------|---------|
| D1 | admin 在 proj-alpha 下创建 session "sess-1" | 201 |
| D2 | user2 GET /api/sessions/{sess-1 id} | 403 |
| D3 | user2 在 proj-beta 下创建 session "sess-2" | 201 |
| D4 | admin GET /api/sessions/{sess-2 id} | 403 |

## Group E — Admin 接口

| ID | 操作 | 预期结果 |
|----|------|---------|
| E1 | admin GET /api/admin/users | 200，包含两个用户 |
| E2 | user2 GET /api/admin/users | 403 |
| E3 | admin PATCH /api/admin/users/{user2 id}/features {features:15} | 200 |

## Group F — 限流 / 防爆破

| ID | 操作 | 预期结果 |
|----|------|---------|
| F1 | 同 IP 60s 内第二次 POST /api/auth/send-verification（相同邮箱） | 429 please wait 60 seconds |
| F2 | 同 IP 1 分钟内连续 11 次 POST /api/auth/login（不同邮箱） | 第 11 次返回 429 too many login attempts |
| F3 | 同一邮箱连续 5 次密码错误登录，第 6 次 | 429 account temporarily locked |

## Group G — Marketplace

| ID | 操作 | 预期结果 |
|----|------|---------|
| G1 | GET /api/marketplace/packages，带 admin token | 200，返回 [] （无云端配置时返回空列表，不报错） |
| G2 | GET /api/marketplace/installed-mcps | 200，返回列表 |
| G3 | GET /api/marketplace/installed-skills | 200，返回列表 |

## Group H — 前端页面（Chrome MCP）

| ID | 操作 | 预期结果 |
|----|------|---------|
| H1 | 浏览器访问 http://localhost:9040 | 弹出登录/注册弹框，无 JS 错误 |
| H2 | 点击"注册"，填邮箱→发送验证码→填验证码+密码→注册 | 成功进入主界面，左下角显示"退出登录" |
| H3 | 刷新页面 | token 持久化，无需重新登录，直接进入主界面 |
| H4 | 点击"退出登录" | 清除 token，重新显示登录弹框 |
| H5 | 以 features=0 的普通用户登录 | Task/Asset/Workflow/Terminal 按钮不可见 |
| H6 | 导航到 /marketplace | 页面正常加载，显示"No packages found"，无错误 toast |

---

## 测试账号（运行时填入）

| 变量 | 值 |
|------|-----|
| ADMIN_TOKEN | （A2 注册后获取）|
| USER2_TOKEN | （C1 注册后获取）|
| ADMIN_ID | （A6 me 接口获取）|
| USER2_ID | （E1 admin list 获取）|
| PROJ_ALPHA_ID | （C2 创建后获取）|
| PROJ_BETA_ID | （C5 创建后获取）|
| SESS1_ID | （D1 创建后获取）|
| SESS2_ID | （D3 创建后获取）|

---

## 尚未覆盖（需特殊环境）

| 功能 | 前提 |
|------|------|
| Docker 容器生命周期 | 需要 Docker daemon + boxnex-agent 镜像 |
| Agent 执行（WebSocket 聊天） | 需要 Anthropic API Key |
| WebSocket 终端 | PTY 环境 |
| Token 计费全链路 | 需要 agent 实际执行 |
| Git 操作（status/commit/diff） | 需要有 git_repo_path 的 project |
| MySQL 模式 | 需要 MySQL 实例 |
| Resend 真实发信 | 需要配置 resend_api_key + 真实域名 |
