# 移动端浏览器与 PWA

NexBox 的同一套 Web 界面可在桌面和移动端浏览器中使用。服务器部署时，通过该实例的地址访问；本地部署时，`127.0.0.1` 指向当前设备，需要根据访问方式选择监听地址或隧道。

前端包含 `manifest.webmanifest`、应用图标和独立窗口显示配置。支持的浏览器可使用“添加到主屏幕”或“安装应用”入口。安装入口是否出现由浏览器和访问环境决定。

当前没有离线执行模式：浏览器仍需连接 NexBox 服务，对话由服务所在机器上的 CLI 执行。PWA 不会把 Agent 安装到手机，也不是 Electron 或原生客户端。

Android TWA 的域名关联取决于具体部署的域名、Android 包名和签名。仓库中的 `web/public/.well-known/assetlinks.json` 是原有 Android 包的关联配置；自行发布 Android 包时需替换为自己的关联信息。NexBox 不提供通用 APK 构建或发布流程。
