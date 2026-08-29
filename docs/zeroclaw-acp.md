# ZeroClaw 极简部署维护手册

默认部署直接使用默认值，不需要额外的 Nexbox 环境变量或进程管理配置。

只需满足两点：

- `zeroclaw` 命令在 `PATH` 中
- ZeroClaw 中有且只有一个启用的 Agent

## 首次部署

### 1. 构建 Nexbox

```bash
cd web
npm ci
cd ..
./build.sh
```

构建结果是：

```text
dist/nexbox
```

### 2. 安装 ZeroClaw

```bash
git clone https://github.com/zeroclaw-labs/zeroclaw.git
cd zeroclaw
./install.sh --prebuilt --skip-quickstart
```

安装后确认：

```bash
zeroclaw --version
```

### 3. 配置 OpenRouter

确认环境变量 `OPEN_ROUTER_KEY` 已存在，然后执行：

```bash
zeroclaw quickstart \
  --model-provider openrouter \
  --model minimax/minimax-m3:free \
  --api-key "$OPEN_ROUTER_KEY"
```

Quickstart 会创建一个默认 Agent，并保存 OpenRouter 配置。Agent 的名字不重要；只有一个 Agent 时，Nexbox 会自动使用它。

验证 OpenRouter：

```bash
zeroclaw agent -m "Reply with exactly: OK"
```

### 4. 启动 Nexbox

首次使用先创建登录用户：

```bash
./dist/nexbox add-user <用户名> <密码>
```

然后启动：

```bash
./dist/nexbox
```

浏览器打开 `http://127.0.0.1:9040`，创建会话时选择 `ZeroClaw` 即可。

不需要单独启动 `zeroclaw daemon` 或 `zeroclaw acp`。

## 更换模型

查看当前模型：

```bash
zeroclaw config get providers.models.openrouter.default.model
```

切换模型：

```bash
zeroclaw config set providers.models.openrouter.default.model <OpenRouter模型ID>
```

例如：

```bash
zeroclaw config set providers.models.openrouter.default.model minimax/minimax-m3:free
```

下一次新请求就会使用新模型，不需要修改 Nexbox。

## 更换 OpenRouter Key

```bash
zeroclaw config set providers.models.openrouter.default.api_key
```

ZeroClaw 会提示输入新 Key，并进行隐藏输入。不要把 Key 写进 Nexbox 配置或仓库。

## 日常检查

```bash
zeroclaw doctor
zeroclaw agent -m "Reply with exactly: OK"
```

如果 Nexbox 找不到 ZeroClaw，检查：

```bash
command -v zeroclaw
```

## 更新

更新 Nexbox：

```bash
git pull
cd web
npm ci
cd ..
./build.sh
```

更新 ZeroClaw：

```bash
cd zeroclaw
git pull
./install.sh --prebuilt --skip-quickstart
```

更新完成后重启 Nexbox 进程即可。
