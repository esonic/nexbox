# NexBox 安卓应用打包指南 (PWA + TWA)

将 `https://box-nex.top` 包装成一个安卓应用,使用 Trusted Web Activity (TWA)。
应用本质是一个全屏 Chrome 容器,**网站更新后无需重新发布 APK**(除非动了
应用名、图标、包名、目标域名或升级 TWA SDK)。

## 仓库内已经准备好的东西

- `web/public/manifest.webmanifest` — PWA 清单
- `web/public/icons/icon-192.png`、`icon-512.png`、`icon-maskable-512.png`
- `web/index.html` 已引用 manifest 与 theme-color
- `web/public/.well-known/assetlinks.json` — 占位文件,等待填入签名指纹
- `server/frontend.go` 的 `init()` 注册了 `.webmanifest` 的 MIME 类型

## 打包步骤

### 1. 构建并部署前端

```bash
./build.sh   # 构建前端 + 嵌入到 server 二进制
# 部署 server,确认以下 URL 都可访问:
#   https://box-nex.top/manifest.webmanifest
#   https://box-nex.top/icons/icon-192.png
#   https://box-nex.top/.well-known/assetlinks.json
```

### 2. 安装 Bubblewrap

```bash
# 需要 Node 16+ 与 JDK 17
npm i -g @bubblewrap/cli
```

首次运行会自动下载 Android SDK 和 JDK,大约几分钟。

### 3. 初始化 TWA 工程

```bash
mkdir -p ~/nexbox-twa && cd ~/nexbox-twa
bubblewrap init --manifest=https://box-nex.top/manifest.webmanifest
```

交互式问题中需要确认:
- **Application ID (package name)**:推荐 `top.box_nex.twa`(与 `assetlinks.json`
  里的 `package_name` 保持一致;域名反写)
- **Launcher name**:`NexBox`
- **Display mode**:`standalone`
- **Orientation**:`any`
- **Theme color**:`#1D4ED8`
- **Status bar color**:`#1D4ED8`
- **Signing key 路径**:默认 `./android.keystore`,**这个文件以后必须保存好,
  丢了就无法发布新版本**

### 4. 构建 APK / AAB

```bash
bubblewrap build
```

产物:
- `app-release-signed.apk` — 可直接侧载到手机
- `app-release-bundle.aab` — 上架 Google Play 用

### 5. 填写 assetlinks.json

`bubblewrap build` 完成后会打印 keystore 的 SHA256 指纹,形如:

```
SHA256 Fingerprint: AB:CD:EF:01:...:99
```

把这个值复制到 `web/public/.well-known/assetlinks.json` 里替换
`REPLACE_WITH_SHA256_FROM_BUBBLEWRAP`,然后重新 `./build.sh` 并部署。

也可以直接用 bubblewrap 生成:

```bash
bubblewrap fingerprint
```

确认 `https://box-nex.top/.well-known/assetlinks.json` 返回的内容包含
正确指纹,**Content-Type 必须是 `application/json`**(Go 的默认 MIME 表会
正确识别)。

> 没有正确配置 assetlinks.json,应用打开时顶部会显示 Chrome 地址栏
> (Custom Tab fallback),功能仍可用但观感差。

### 6. 安装测试

```bash
adb install ./app-release-signed.apk
```

或者把 APK 发到手机直接安装(需要在系统设置里允许"未知来源")。

## 升级流程

| 改动类型 | 操作 |
| --- | --- |
| 网站内容更新(HTML/JS/CSS、API 行为) | `./build.sh` 部署即可,APP 自动看到 |
| 应用名、图标、theme color | 改 `manifest.webmanifest` → `bubblewrap update` → 重新发版 |
| 包名、签名 | 几乎等同重新发布一个新应用,慎改 |
| 目标域名 | 改 `manifest.webmanifest` 的 `start_url` + 新域名重新部署
  `assetlinks.json` + `bubblewrap update` |

## 注意事项

- **HTTPS 必备**:TWA 强制 HTTPS,证书必须有效。`box-nex.top` 已经满足。
- **WebSocket** (`/ws`) 在 TWA 内完全等同于 Chrome,无需特殊处理。
- **Cookie / 登录态**:TWA 共享 Chrome 的 cookie jar,所以在浏览器里登录过
  之后,打开 APP 是登录态(反之亦然)。
- **Service Worker 不是必须**:Chrome 已经不强制要求,目前我们没有加 SW
  (在线应用也不需要离线缓存)。
- **iOS**:同样的 manifest 可以让 iOS 用户通过 Safari "添加到主屏幕"
  得到一个全屏 web app,但 App Store 上架需要另写 WKWebView 包装。
