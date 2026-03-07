# 创建变更单 - 设计文档

## 1. 背景

Nexbox 需要集成 OKOne 平台，实现自动创建变更单。整体流程包括：
1. 从 Chrome 的 localStorage 自动读取 OKOne 认证 token
2. 从 OKOne 获取最新的编译产物（artifact）
3. 使用 Claude 根据迭代上下文自动生成变更描述
4. 调用 OKOne API 创建变更单

---

## 2. OKOne API 分析

### API 1：获取编译产物

```
GET /okone/priapi/v1/artifacts/service-environment
  ?serviceEnvironmentId={id}&page=1&limit=100
```

关键响应字段（取自 `rows[0]`）：
| 字段 | 示例 | 用途 |
|------|------|------|
| `id` | `942882` | 作为变更单中的 `artifactId` |
| `name` | `sre-ok-issue-insight-platform_prod` | 产物展示名 |
| `commitId` | `9e4606747c01...` | 关联的 commit |
| `branch` | `main` | 分支信息 |
| `createdAt` | `2026-02-10T03:00:17.710Z` | 构建时间 |
| `meta.tag` | `infra/.../platform_prod:main-9e460674-...` | 镜像 tag |

### API 2：创建变更单

```
POST /okone/priapi/v1/change/management/order/insertOrUpdate
```

请求体字段分类：

#### A. 项目级配置（用户在项目设置中一次性填写）

| 字段 | 示例 | 说明 |
|------|------|------|
| `serviceGroupId` | `"120083"` | = applicationId |
| `applicationId`（header） | `120083` | OKOne 应用 ID |
| `entityId`（header） | `120083` | = applicationId |
| `serviceId` | `2586` | 服务数字 ID |
| `service` | `"sre-issueinsight-service"` | 服务标识符 |
| `serviceName` | `"sre-issueinsight-service"` | 与 service 相同 |
| `serviceEnvironmentId` | `103061` | 服务环境数字 ID |
| `serviceEnvironmentName` | `"okx-sre-issueinsight-service-pro"` | 服务环境展示名 |
| `owner` | `"eric.du@okg.com"` | 变更负责人邮箱 |

#### B. 运行时动态生成

| 字段 | 来源 | 说明 |
|------|------|------|
| `title` | `iteration.title` | 取自当前迭代标题 |
| `startTime` | `time.Now()` | 格式：`"2026-02-10 15:14"` |
| `createdAt` | `time.Now().UnixMilli()` | 毫秒级时间戳 |
| `artifactId` | API 1 响应 | 最新产物的 `rows[0].id` |
| `artifactDisplayName` | API 1 响应 | 由 name + commitId 短码 + 日期拼接 |
| `securityCheckFailedReason` | 硬编码模板 | `{artifactId: "(PASSED) SAST: ..."}` |
| `securityCheckFailedStage` | 硬编码 | `{artifactId: "SAST"}` |

#### C. 固定常量

| 字段 | 值 |
|------|-----|
| `changeType` | `"Standard"` |
| `configType` | `"Service"` |
| `itemType` | `"Service"` |
| `simulateType` | `"publish"` |
| `securityCheckStatus` | `"true"` |
| `fireApproval` | `true` |
| `hitGray` | `false` |
| `changeSequence` | `1` |
| `devTaskConfigs` | `[]` |

#### D. Claude AI 生成（当前限制 100 字以内，中文）

| 字段 | 说明 |
|------|------|
| `description` | 变更描述 / 摘要 |
| `designDocument` | 功能设计概述 |
| `testQualityReport` | 测试结果总结 |
| `operationManual` | 操作手册 |
| `verificationManual` | 验证步骤 |
| `changeRisk` | 风险评估 |
| `rollbackManual` | 回滚方案 |

### 必需的请求头

| Header | 值 | 说明 |
|--------|-----|------|
| `authorization` | `Bearer <token>` | **自动从 Chrome localStorage 读取** |
| `applicationid` | 取自项目配置 | 与 serviceGroupId 相同 |
| `entityid` | 取自项目配置 | 与 applicationId 相同 |
| `scope` | `APPLICATION_NEW` | 固定值 |
| `servicename` | `okone` | 固定值 |
| `content-type` | `application/json` | 固定值 |

---

## 3. Token 管理策略

### 方案：自动从 Chrome localStorage 读取

Token 存储在 Chrome 的 localStorage 中（域名 `test-okone.okg.com`，key `ok_okone` JSON 的 `token` 字段）。
Nexbox 后端通过读取 Chrome 的 LevelDB 文件自动获取，用户无需手动操作。

- 实现位于 `server/okone/token.go`（已完成）
- 环境变量 `NEXBOX_CHROME_DEV=1` 控制读取 Chrome Dev 版本
- 内置 JWT 过期检测

```
用户点击 "Create Change"
  │
  ├─ 后端自动从 Chrome localStorage 读取 token
  │
  ├─ token 有效 → 调用 OKOne API → 创建成功
  │
  └─ token 过期或未找到
       │
       └─ 返回错误提示："请在 Chrome 中打开 test-okone.okg.com 并登录"
            用户登录后重试即可
```

---

## 4. 架构设计

### 4.1 数据模型变更

在 `Project` 模型上扩展 OKOne 配置字段（不含 token，token 从 Chrome 读取）：

```go
type Project struct {
    // ... 现有字段 ...

    // OKOne integration
    OkoneBaseURL        string `json:"okone_base_url"`
    OkoneApplicationID  string `json:"okone_application_id"`
    OkoneServiceID      int    `json:"okone_service_id"`
    OkoneService        string `json:"okone_service"`
    OkoneServiceName    string `json:"okone_service_name"`
    OkoneServiceEnvID   int    `json:"okone_service_env_id"`
    OkoneServiceEnvName string `json:"okone_service_env_name"`
    OkoneOwner          string `json:"okone_owner"`
}
```

### 4.2 后端模块

```
server/okone/
  token.go       -- 从 Chrome localStorage 读取 token（已完成）
  client.go      -- OKOne API HTTP 客户端
```

### 4.3 后端 API

```
POST /api/iterations/{iterationId}/create-change-order
```

无需请求体。

响应（成功）：
```json
{
  "order_id": 59690
}
```

响应（token 过期）：
```json
{
  "error": "OKOne token has expired. Please refresh test-okone.okg.com in Chrome to get a new token"
}
```

### 4.4 完整流程

```
POST /create-change-order
  │
  ├─ 1. 加载 iteration + project
  ├─ 2. 校验项目已配置 OKOne 信息
  ├─ 3. 从 Chrome localStorage 自动读取 token
  │     └─ 若过期/未找到 → 返回错误提示用户去 Chrome 登录
  ├─ 4. 调用 OKOne API 1：获取最新编译产物
  │     └─ 若 401 → 返回 token 过期错误
  ├─ 5. 收集迭代上下文（requirement、diff、assets）
  ├─ 6. 调用 Claude 生成 7 个描述字段（中文，各 100 字以内）
  ├─ 7. 组装完整请求体
  ├─ 8. 调用 OKOne API 2：创建变更单
  │     └─ 若 401 → 返回 token 过期错误
  └─ 9. 返回变更单 ID
```

### 4.5 前端变更

#### 项目设置弹窗

新增 "OKOne Integration" 区域：
- Base URL（带默认值 `https://test-okone.okg.com`）
- Application ID
- Service ID / Service / Service Name
- Service Environment ID / Service Environment Name
- Owner 邮箱

#### "Create Change" 按钮

点击后：
1. 检查项目是否已配置 OKOne → 若未配置，提示去 Settings 配置
2. 调用 `POST /api/iterations/{id}/create-change-order`
3. 显示加载状态
4. 成功 → 弹出气泡显示变更单 ID
5. token 过期 → 显示错误 "请在 Chrome 中登录 OKOne 后重试"
6. 其他错误 → 显示错误信息
