# NexBox Plugin System — 完整设计文档

## 一、当前架构（v1）

### 1.1 总体结构

```
plugins/
├── sdk/                         # Plugin SDK（类型定义包）
│   ├── index.ts                 # 核心类型导出
│   └── globals.d.ts             # window.__nexbox__ 全局类型
├── example/                     # 外部插件示例 / 模板
│   ├── src/plugin.ts
│   ├── src/HelloPanel.tsx
│   └── vite.config.ts
├── create-plugin.mjs            # 脚手架工具
└── GUIDE.md                     # 开发指南

web/src/plugins/
├── index.ts                     # 公共导出聚合
├── runtime.ts                   # 插件运行时（注册、激活、命令、hooks）
├── slots.tsx                    # Slot 工厂 + 预定义扩展点
├── PanelHost.tsx                # 面板渲染宿主
├── loader.ts                    # 插件发现与加载
├── okone/plugin.ts              # 内置插件：OKOne 变更单
├── brainstorm/plugin.ts         # 内置插件：脑图
├── asset/plugin.ts              # 内置插件：资产管理
└── task/plugin.ts               # 内置插件：任务管理
```

### 1.2 Plugin SDK 类型定义

SDK 是插件开发的类型基础，宿主和外部插件共同引用。

#### 核心接口

```typescript
// Disposable — 清理句柄
interface Disposable {
  dispose: () => void;
}

// 插件定义
interface Plugin {
  id: string;
  name: string;
  source?: "builtin" | "installed";
  contributes: PluginContributes;
  activate?: (ctx: PluginContext) => Disposable | void;
  deactivate?: () => void;
}

// 贡献声明
interface PluginContributes {
  slots?: SlotContribution[];
  panels?: PanelContribution[];
  commands?: CommandContribution[];
  menus?: Record<string, MenuEntry[]>;
}
```

#### 三种扩展类型

**Slot — UI 插槽注入：**
```typescript
interface SlotContribution {
  slot: string;              // 插槽标识，如 "git.menuItems"
  component: ComponentType<any>;
  order?: number;
}
```

**Panel — 工作区面板：**
```typescript
interface PanelContribution {
  id: string;
  label: string;             // 工具栏按钮文字
  icon: ComponentType<{ className?: string }>;
  title: string;             // 面板标题
  component: ComponentType<WorkspacePanelProps>;
  order?: number;            // 排序权重
}

interface WorkspacePanelProps {
  projectId: string;
}
```

**Command — 命令：**
```typescript
interface CommandContribution {
  id: string;
  title: string;
  icon?: ComponentType<{ className?: string }>;
}

type CommandHandler = (args?: any) => unknown | Promise<unknown>;
```

#### PluginContext

插件激活时获得的上下文对象：

```typescript
interface PluginContext {
  commands: {
    register: (id: string, handler: CommandHandler) => Disposable;
    execute: (id: string, args?: unknown) => Promise<unknown>;
  };
  host: {
    navigate: (path: string) => void;
    notify: (message: string, level?: "info" | "success" | "error") => void;
  };
  subscriptions: Disposable[];  // 自动清理列表
}
```

#### NexboxRuntime（window.__nexbox__）

```typescript
interface NexboxRuntime {
  registerPlugin: (plugin: Plugin) => void;
  executeCommand: (id: string, args?: unknown) => Promise<unknown>;
  libs: Record<string, unknown>;  // 共享依赖：react, zustand, lucide-react
}
```

### 1.3 运行时（runtime.ts）

运行时是插件系统的核心，提供以下功能：

#### 命令注册表

```typescript
const commandHandlers = new Map<string, CommandHandler>();

// 注册命令（自动加插件 ID 前缀）
function registerCommand(id: string, handler: CommandHandler): Disposable;
// 执行命令
async function executeCommand(id: string, args?: unknown): Promise<unknown>;
// 查询命令
function hasCommand(id: string): boolean;
```

#### 宿主服务注入

```typescript
interface HostServices {
  navigate: (path: string) => void;
  notify: (message: string, level?: "info" | "success" | "error") => void;
}

// HostBridge 组件：置于 Router 内部，自动注入 useNavigate()
function HostBridge(): null;
```

#### 插件生命周期

```typescript
// 注册插件 → 加入列表 → 若未禁用则激活
function registerPlugin(plugin: Plugin): void;

// 注销插件 → 停用 → 从列表移除
function unregisterPlugin(pluginId: string): void;

// 激活流程：创建 PluginContext → 调用 plugin.activate(ctx)
function activatePlugin(plugin: Plugin): void;

// 停用流程：plugin.deactivate() → dispose disposables → 清理 context
function deactivatePlugin(plugin: Plugin): void;

// 启用/禁用（保留注册，切换激活状态）
function enablePlugin(pluginId: string): void;
function disablePlugin(pluginId: string): void;
```

#### 禁用状态持久化

使用 `localStorage("nexbox:disabled-plugins")` 保存禁用列表，页面刷新后保持禁用状态。

#### 响应式 React Hooks

```typescript
// 所有插件（包含禁用的）
function useAllPlugins(): Plugin[];

// 仅活跃插件（排除禁用和失败的）
function usePlugins(): Plugin[];

// 面板列表（从活跃插件聚合，按 order 排序）
function usePanels(): PanelContribution[];

// 禁用插件 ID 集合
function useDisabledPluginIds(): Set<string>;
```

所有 hooks 基于 `useSyncExternalStore`，在插件注册/注销/启用/禁用时自动触发 React 重渲染。

#### 共享依赖暴露

```typescript
window.__nexbox__ = {
  registerPlugin,
  executeCommand,
  libs: {
    react: React,
    "react/jsx-runtime": jsxRuntime,
    zustand,
    "lucide-react": lucideReact,
  },
};
```

### 1.4 Slot 系统（slots.tsx）

#### createSlot 工厂函数

```typescript
function createSlot<TProps>(name: string): SlotDefinition<TProps>;

interface SlotDefinition<TProps> {
  name: string;
  Render: ComponentType<TProps & { wrapper?; errorFallback? }>;
  use: () => { id: string; Component: ComponentType<TProps> }[];
}
```

工作原理：
1. `Render` 组件内部调用 `usePlugins()` 获取活跃插件
2. 过滤出 `contributes.slots` 中 `slot === name` 的贡献
3. 每个贡献用 `PluginErrorBoundary` 包裹渲染
4. 支持 `wrapper` 自定义外层容器

#### 预定义扩展点

```typescript
// Git 操作菜单项
const GitMenuItemsSlot = createSlot<{ projectId: string; onClose: () => void }>("git.menuItems");

// Git 结果区域
const GitResultsSlot = createSlot<{ projectId: string }>("git.results");
```

#### 错误隔离

每个插件组件被 `PluginErrorBoundary` 包裹：
- 捕获渲染错误，不影响宿主
- DEV 模式显示错误提示，可点击重试
- PROD 模式静默隐藏

### 1.5 面板宿主（PanelHost.tsx）

```typescript
const PanelHost = {
  Buttons: PanelButtons,    // 工具栏按钮（遍历 usePanels()）
  Content: PanelContent,    // 面板内容（按 activePanel 匹配渲染）
  usePanelTitle,            // 获取面板标题
};
```

- `PanelButtons`：遍历所有面板贡献，渲染工具栏按钮
- `PanelContent`：根据 `activePanel` 查找对应面板组件，用 `PanelErrorBoundary` 包裹渲染
- 面板只支持 **右侧面板** 一个位置

### 1.6 加载器（loader.ts）

#### 内置插件加载

```typescript
// Vite glob 自动发现 web/src/plugins/*/plugin.ts
const builtinModules = import.meta.glob("./*/plugin.ts", { eager: true });

function loadBuiltinPlugins(): void {
  // 遍历 modules → 提取 default export 或 *Plugin 命名 → registerPlugin()
}
```

#### 外部插件加载

```typescript
// 从 /plugins/plugins.json 读取清单
// 通过 <script> 标签动态加载 .js 文件
// 插件 JS 内部调用 window.__nexbox__.registerPlugin() 完成注册

async function loadInstalledPlugins(): Promise<void>;
```

#### 插件管理 API

```typescript
// 获取已安装插件清单
async function fetchInstalledPlugins(): Promise<InstalledPluginEntry[]>;
// 切换启用/禁用
async function toggleInstalledPlugin(name: string, enabled: boolean): Promise<boolean>;
// 动态加载
async function loadInstalledPlugin(name: string): Promise<void>;
// 卸载
function unloadInstalledPlugin(pluginId: string, scriptName: string): void;
// 上传安装 ZIP
async function installPluginZip(file: File): Promise<string>;
// 完整卸载（删除文件 + 注销运行时）
async function uninstallPlugin(name: string, pluginId?: string): Promise<boolean>;
```

#### 初始化流程

```typescript
async function initPlugins(): Promise<void> {
  loadBuiltinPlugins();           // 1. 同步加载内置插件
  await loadInstalledPlugins();   // 2. 异步加载外部插件
}
```

### 1.7 内置插件一览

| 插件 ID | 名称 | 贡献类型 | 说明 |
|---------|------|---------|------|
| `okone` | OKOne Change Order | 2 slots + 1 command | Git 菜单注入变更单创建入口 |
| `brainstorm` | Brainstorm | 1 panel | 脑图面板 |
| `asset` | Assets | 1 panel | 资产管理面板 |
| `task` | Tasks | 1 panel | 任务管理面板 |

### 1.8 外部插件开发流程

1. **脚手架创建**：`node plugins/create-plugin.mjs my-tool`
2. **开发**：`npm run dev`（Vite watch 模式）
3. **构建**：`npm run pack`（输出 .zip）
4. **安装**：Settings → Plugins → Install Plugin → 上传 .zip
5. **构建格式**：IIFE，共享依赖通过 `window.__nexbox__.libs` 引用

### 1.9 Settings 管理页面

`PluginsSettings.tsx` 提供完整的插件管理 UI：
- 分区显示 Built-in 和 Installed 插件
- Toggle 开关控制启用/禁用
- 显示贡献统计（N slots, N panels, N commands）
- 支持上传 .zip 安装、卸载已安装插件
- 实时反映运行时状态变化

---

## 二、架构优劣分析

### 2.1 优点

1. **类型安全**：SDK 提供完整的 TypeScript 类型定义，宿主和插件共享同一套接口
2. **生命周期管理**：activate/deactivate + Disposable 自动清理，资源管理清晰
3. **错误隔离**：PluginErrorBoundary 防止单个插件崩溃影响整个应用
4. **响应式 UI**：基于 useSyncExternalStore 的 hooks，插件变更即时反映到 UI
5. **扩展性**：Slot + Panel + Command 三种扩展点覆盖常见需求
6. **开发体验**：脚手架工具 + 模板 + 热重载支持
7. **灵活管理**：运行时启用/禁用不需要重启，禁用状态跨刷新持久化

### 2.2 弱点

1. **无插件间通信**：插件只能通过 executeCommand 间接交互，没有事件发布/订阅机制
2. **Slot 不可扩展**：扩展点必须由宿主预定义，插件无法创建自己的扩展点供其他插件使用
3. **外部插件加载机制落后**：使用 `<script>` + 全局变量，存在作用域污染、无模块系统
4. **依赖版本盲兼容**：共享依赖版本固定暴露，外部插件无法声明兼容范围
5. **无配置系统**：插件没有标准化的配置声明和 UI
6. **UI 位置单一**：PanelHost 仅支持右侧面板，无法向侧边栏、底部、状态栏等位置贡献 UI

---

## 三、v2 改造方案

### 3.1 事件总线（插件间通信）

#### 问题

当前插件只能通过 `executeCommand` 间接交互。插件 A 无法监听插件 B 的状态变化，没有发布/订阅机制。

#### 设计

在 `PluginContext` 上新增 `events` API，提供类型安全的事件发布/订阅。

**SDK 类型扩展：**

```typescript
interface PluginEvents {
  /** 发布事件。短名称自动加插件 ID 前缀 */
  emit(event: string, data?: unknown): void;
  /** 订阅事件。返回取消订阅函数 */
  on(event: string, handler: EventHandler): Disposable;
  /** 一次性订阅 */
  once(event: string, handler: EventHandler): Disposable;
}

type EventHandler = (data: unknown, meta: EventMeta) => void;

interface EventMeta {
  source: string;     // 发布方插件 ID
  event: string;      // 事件全名
  timestamp: number;
}

// PluginContext 扩展
interface PluginContext {
  commands: CommandsAPI;
  events: PluginEvents;   // ← 新增
  host: HostAPI;
  subscriptions: Disposable[];
}
```

**EventBus 实现：**

```typescript
class EventBus {
  private handlers = new Map<string, Set<EventHandler>>();

  emit(event: string, data?: unknown, source?: string): void {
    const meta: EventMeta = { source: source ?? "host", event, timestamp: Date.now() };
    // 精确匹配
    this.handlers.get(event)?.forEach((h) => h(data, meta));
    // 通配符匹配：订阅 "okone.*" 可以收到 "okone.orderCreated"
    for (const [pattern, handlers] of this.handlers) {
      if (pattern.endsWith(".*")) {
        const prefix = pattern.slice(0, -2);
        if (event.startsWith(prefix + ".") && event !== pattern) {
          handlers.forEach((h) => h(data, meta));
        }
      }
    }
  }

  on(event: string, handler: EventHandler): Disposable {
    if (!this.handlers.has(event)) this.handlers.set(event, new Set());
    this.handlers.get(event)!.add(handler);
    return { dispose: () => { this.handlers.get(event)?.delete(handler); } };
  }

  once(event: string, handler: EventHandler): Disposable {
    const wrapper: EventHandler = (data, meta) => { disposable.dispose(); handler(data, meta); };
    const disposable = this.on(event, wrapper);
    return disposable;
  }
}
```

**集成到 PluginContext：**

```typescript
function createPluginContext(pluginId: string): PluginContext {
  const subscriptions: Disposable[] = [];
  return {
    events: {
      emit: (event, data) => {
        const fullEvent = event.includes(".") ? event : `${pluginId}.${event}`;
        eventBus.emit(fullEvent, data, pluginId);
      },
      on: (event, handler) => {
        const d = eventBus.on(event, handler);
        subscriptions.push(d);
        return d;
      },
      once: (event, handler) => {
        const d = eventBus.once(event, handler);
        subscriptions.push(d);
        return d;
      },
    },
    commands: { /* existing */ },
    host: { /* existing */ },
    subscriptions,
  };
}
```

**使用示例：**

```typescript
// okone 插件发布事件
activate(ctx) {
  ctx.commands.register("createChangeOrder", async (args) => {
    const result = await createOrder(args);
    ctx.events.emit("orderCreated", { orderId: result.id });
  });
}

// 另一个插件监听
activate(ctx) {
  ctx.events.on("okone.orderCreated", (data) => {
    console.log("Order created:", data);
  });
  // 通配符监听
  ctx.events.on("okone.*", (data, meta) => {
    console.log(`okone event: ${meta.event}`, data);
  });
}
```

**宿主侧事件：**

```typescript
eventBus.emit("host.sessionChanged", { sessionId }, "host");
eventBus.emit("host.projectBound", { projectId }, "host");
```

### 3.2 动态 Slot 注册

#### 问题

当前 Slot 必须由宿主用 `createSlot()` 预先定义。插件不能创建自己的扩展点供其他插件使用。

#### 设计

引入 `SlotRegistry` 中心注册表，允许运行时动态创建和发现 Slot。

```typescript
class SlotRegistry {
  private slots = new Map<string, SlotDefinition<any>>();
  private listeners = new Set<() => void>();

  /** 注册一个 Slot（宿主或插件均可） */
  define<TProps>(name: string): SlotDefinition<TProps> {
    if (this.slots.has(name)) return this.slots.get(name)!;
    const slot = createSlot<TProps>(name);
    this.slots.set(name, slot);
    this.notify();
    return slot;
  }

  get<TProps>(name: string): SlotDefinition<TProps> | undefined {
    return this.slots.get(name);
  }

  list(): string[] {
    return [...this.slots.keys()];
  }

  /** React hook：响应式获取 Slot */
  useSlot<TProps>(name: string): SlotDefinition<TProps> | undefined {
    useSyncExternalStore(
      (cb) => { this.listeners.add(cb); return () => this.listeners.delete(cb); },
      () => this.slots.get(name),
    );
    return this.slots.get(name);
  }

  private notify() { this.listeners.forEach((cb) => cb()); }
}

export const slotRegistry = new SlotRegistry();

// 宿主预定义的 Slot（向后兼容）
export const GitMenuItemsSlot = slotRegistry.define<{ projectId: string; onClose: () => void }>("git.menuItems");
export const GitResultsSlot = slotRegistry.define<{ projectId: string }>("git.results");
```

**PluginContext 集成：**

```typescript
slots: {
  define: (name) => {
    const fullName = name.includes(".") ? name : `${pluginId}.${name}`;
    return slotRegistry.define(fullName);
  },
  get: (name) => slotRegistry.get(name),
},
```

**使用示例：**

```typescript
// brainstorm 插件定义自己的扩展点
activate(ctx) {
  ctx.slots.define<{ mindmapId: string }>("toolbar");
}

// brainstorm 面板中使用
function BrainstormPanel({ mindmapId }) {
  const ToolbarSlot = slotRegistry.get("brainstorm.toolbar");
  return (
    <div>
      {ToolbarSlot && <ToolbarSlot.Render mindmapId={mindmapId} />}
    </div>
  );
}

// 第三方插件贡献
const myPlugin: Plugin = {
  id: "mindmap-export",
  contributes: {
    slots: [{ slot: "brainstorm.toolbar", component: ExportButton }],
  },
};
```

### 3.3 ESM + Import Maps 外部插件加载

#### 问题

当前通过 `<script>` + `window.__nexbox__` 全局变量加载外部插件。存在全局作用域污染、无模块系统、依赖传递靠 hack。

#### 设计

改用浏览器原生 ES Module + Import Maps，插件是标准 ESM 文件。

**Import Maps 配置（index.html）：**

```html
<script type="importmap">
{
  "imports": {
    "react": "/vendor/react.esm.js",
    "react/jsx-runtime": "/vendor/react-jsx-runtime.esm.js",
    "react-dom": "/vendor/react-dom.esm.js",
    "zustand": "/vendor/zustand.esm.js",
    "lucide-react": "/vendor/lucide-react.esm.js",
    "@nexbox/plugin-sdk": "/vendor/nexbox-plugin-sdk.esm.js"
  }
}
</script>
```

**Vite 构建插件：**

```typescript
// vite.config.ts
import { generateImportMap } from "./build/import-map-plugin";

export default defineConfig({
  plugins: [
    generateImportMap({
      shared: ["react", "react/jsx-runtime", "zustand", "lucide-react"],
      sdk: "@nexbox/plugin-sdk",
    }),
  ],
});
```

**Plugin SDK 新增 definePlugin：**

```typescript
export function definePlugin(plugin: Plugin): Plugin {
  if (!plugin.id) throw new Error("Plugin must have an id");
  if (!plugin.contributes) plugin.contributes = {};
  return plugin;
}
```

**Loader 改造：**

```typescript
interface PluginManifest {
  id: string;
  name: string;
  version: string;
  main: string;                  // ESM 入口，如 "index.mjs"
  enabled?: boolean;
  sdkVersion?: string;
  peerDependencies?: Record<string, string>;
}

const loadedModules = new Map<string, { module: any; manifest: PluginManifest }>();

async function loadESMPlugin(manifest: PluginManifest): Promise<void> {
  if (loadedModules.has(manifest.id)) return;

  if (manifest.sdkVersion && !checkSDKCompat(manifest.sdkVersion)) {
    console.warn(`[ext] "${manifest.id}" requires SDK ${manifest.sdkVersion}, skipping.`);
    return;
  }

  try {
    const url = `/plugins/${manifest.id}/${manifest.main}`;
    const mod = await import(/* @vite-ignore */ url);
    const plugin: Plugin = mod.default ?? mod.plugin;
    if (!plugin?.id) {
      console.error(`[ext] "${manifest.id}" has no valid export`);
      return;
    }
    plugin.source = "installed";
    loadedModules.set(manifest.id, { module: mod, manifest });
    registerPlugin(plugin);
  } catch (err) {
    console.error(`[ext] failed to load "${manifest.id}":`, err);
  }
}
```

**外部插件项目结构：**

```
my-plugin/
├── package.json
├── vite.config.ts
├── manifest.json
└── src/
    ├── index.ts          ← ESM 入口
    └── MyPanel.tsx
```

```json
// manifest.json
{
  "id": "my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "main": "index.mjs",
  "sdkVersion": "^2.0.0",
  "peerDependencies": { "react": "^19.0.0" }
}
```

```typescript
// src/index.ts
import { definePlugin } from "@nexbox/plugin-sdk";
import { MyPanel } from "./MyPanel";

export default definePlugin({
  id: "my-plugin",
  name: "My Plugin",
  contributes: {
    panels: [{
      id: "my-panel",
      label: "My Panel",
      icon: () => <span>🔧</span>,
      title: "My Panel",
      component: MyPanel,
    }],
  },
});
```

```typescript
// vite.config.ts — 外部插件构建
export default defineConfig({
  build: {
    lib: { entry: "src/index.ts", formats: ["es"], fileName: () => "index.mjs" },
    rollupOptions: {
      external: ["react", "react/jsx-runtime", "zustand", "lucide-react", "@nexbox/plugin-sdk"],
    },
  },
});
```

**向后兼容：**

保留 `window.__nexbox__.registerPlugin` 作为降级方案：

```typescript
export async function initPlugins(): Promise<void> {
  loadBuiltinPlugins();           // 1. 内置插件（同步）
  await loadInstalledPlugins();   // 2. ESM 外部插件（新方式）
  // 3. Legacy <script> 插件仍可通过 window.__nexbox__ 注册
}
```

### 3.4 共享依赖版本管理

#### 问题

`window.__nexbox__.libs` 固定了依赖版本，外部插件必须盲目兼容。升级 React 可能导致所有外部插件崩溃。

#### 设计

**SDK 版本化：**

```typescript
// @nexbox/plugin-sdk/version.ts
export const SDK_VERSION = "2.0.0";
export const SHARED_DEPS = {
  react: "19.0.0",
  zustand: "5.0.0",
  "lucide-react": "0.460.0",
} as const;
```

**插件声明依赖：**

```json
{
  "sdkVersion": "^2.0.0",
  "peerDependencies": {
    "react": "^19.0.0",
    "zustand": "^5.0.0"
  }
}
```

**加载时校验：**

```typescript
import semver from "semver";

function checkSDKCompat(required: string): boolean {
  return semver.satisfies(SDK_VERSION, required);
}

function checkDepsCompat(peerDeps: Record<string, string>): {
  compatible: boolean;
  warnings: string[];
} {
  const warnings: string[] = [];
  let compatible = true;
  for (const [dep, range] of Object.entries(peerDeps)) {
    const actual = SHARED_DEPS[dep as keyof typeof SHARED_DEPS];
    if (!actual) { warnings.push(`Unknown shared dep: ${dep}`); continue; }
    if (!semver.satisfies(actual, range)) {
      warnings.push(`${dep}: requires ${range}, host has ${actual}`);
      compatible = false;
    }
  }
  return { compatible, warnings };
}
```

**不兼容处理策略：**

- SDK 版本不兼容 → 跳过加载，标记为 failed
- 依赖版本不兼容 → 警告日志，标记为 degraded 模式继续运行

### 3.5 插件配置系统

#### 问题

插件没有标准化的配置声明和 UI。okone 插件的配置硬编码在 `ProjectExtra` 里，不是插件自己管理的。

#### 设计

**配置 Schema 声明：**

```typescript
interface ConfigurationProperty {
  type: "string" | "number" | "boolean" | "select";
  title: string;
  description?: string;
  default?: unknown;
  options?: { label: string; value: string }[];  // type=select
  scope?: "global" | "project";
}

interface PluginContributes {
  // ...existing...
  configuration?: {
    title: string;
    properties: Record<string, ConfigurationProperty>;
  };
}
```

**插件使用示例：**

```typescript
const okonePlugin: Plugin = {
  id: "okone",
  contributes: {
    configuration: {
      title: "OKOne Settings",
      properties: {
        "okone.apiEndpoint": {
          type: "string",
          title: "API Endpoint",
          default: "https://okone.example.com/api",
          scope: "global",
        },
        "okone.autoCreateOrder": {
          type: "boolean",
          title: "Auto Create Order",
          default: false,
          scope: "project",
        },
        "okone.environment": {
          type: "select",
          title: "Default Environment",
          options: [
            { label: "Production", value: "prod" },
            { label: "Staging", value: "staging" },
          ],
          default: "staging",
          scope: "project",
        },
      },
    },
  },
};
```

**Config Store：**

```typescript
class PluginConfigStore {
  private cache = new Map<string, unknown>();

  /** 读取配置值，优先级：project → global → default */
  get<T>(key: string, projectId?: string): T | undefined {
    if (projectId) {
      const val = this.readStorage(`nexbox:plugin-config:${projectId}:${key}`);
      if (val !== undefined) return val as T;
    }
    const globalVal = this.readStorage(`nexbox:plugin-config:global:${key}`);
    if (globalVal !== undefined) return globalVal as T;
    return this.getDefault(key) as T | undefined;
  }

  /** 写入配置值 */
  set(key: string, value: unknown, projectId?: string): void {
    const scope = projectId ?? "global";
    localStorage.setItem(`nexbox:plugin-config:${scope}:${key}`, JSON.stringify(value));
  }
}
```

**PluginContext 集成：**

```typescript
config: {
  get: <T>(key: string, projectId?: string) => pluginConfig.get<T>(fullKey, projectId),
  set: (key: string, value: unknown, projectId?: string) => pluginConfig.set(fullKey, value, projectId),
  onChange: (key: string, handler: (value: unknown) => void) => {
    // 监听 StorageEvent
  },
},
```

**Settings UI 自动渲染：**

Settings 页面根据 `contributes.configuration.properties` 的 schema 自动生成表单，支持：
- boolean → checkbox
- string → text input
- number → number input
- select → dropdown
- 按 scope 分区显示（global / per-project）

### 3.6 多位置 UI 扩展

#### 问题

当前 `PanelHost` 只支持右侧面板一个位置。插件无法向侧边栏、底部面板、状态栏、工具栏等位置贡献 UI。

#### 设计

**扩展位置枚举：**

```typescript
type PanelPosition = "right" | "bottom" | "sidebar" | "statusBar" | "toolbar";

interface PanelContribution {
  // ...existing fields...
  position?: PanelPosition;  // 默认 "right"
}
```

**按位置过滤：**

```typescript
function usePanelsByPosition(position: PanelPosition): PanelContribution[] {
  const panels = usePanels();
  return useMemo(
    () => panels.filter((p) => (p.position ?? "right") === position),
    [panels, position],
  );
}
```

**各位置渲染点：**

| 位置 | 说明 | 宿主渲染位置 |
|------|------|-------------|
| `right` | 右侧面板（现有） | SessionWorkspace 右侧区域 |
| `bottom` | 底部面板 | Terminal 旁的 tab 面板 |
| `sidebar` | 侧边栏区域 | AppSidebar 额外区域 |
| `statusBar` | 底部状态栏 | 底部信息栏内 |
| `toolbar` | 顶部工具栏 | 工具栏按钮区域 |

**使用示例：**

```typescript
// 底部面板
const logsPlugin: Plugin = {
  id: "logs",
  contributes: {
    panels: [{
      id: "logs",
      label: "Logs",
      icon: ScrollText,
      title: "Application Logs",
      component: LogViewerPanel,
      position: "bottom",
    }],
  },
};

// 状态栏指示器
const ciPlugin: Plugin = {
  id: "ci-status",
  contributes: {
    panels: [{
      id: "ci-indicator",
      label: "CI",
      icon: Activity,
      title: "CI Status",
      component: CIStatusIndicator,
      position: "statusBar",
    }],
  },
};
```

---

## 四、迁移计划

### Phase 1 — 基础设施

- 实现 EventBus + PluginContext.events
- 实现 PluginConfigStore + PluginContext.config
- Settings 页面增加插件配置表单渲染

### Phase 2 — 扩展能力

- 实现 SlotRegistry 替代静态 Slot
- PanelContribution 增加 position 字段
- SessionWorkspace 支持多位置渲染

### Phase 3 — 外部插件现代化

- 构建 Import Map 生成工具
- Loader 支持 ESM dynamic import
- 发布 @nexbox/plugin-sdk 独立包
- 依赖版本校验逻辑
- 提供外部插件开发模板（Vite 模板仓库）

### 向后兼容策略

- 所有新增字段使用可选类型（`position?: PanelPosition`），默认值保持现有行为
- `window.__nexbox__` 保留，旧式 `<script>` 插件继续可用
- 现有内置插件无需修改即可在新架构下运行
- `createSlot()` 函数签名不变，内部改为通过 `SlotRegistry.define()` 实现
