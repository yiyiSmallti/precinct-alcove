# Alcove 首期执行 Spec

版本 0.1 · 对应 PRD 0.1

## 1. 技术栈

- Go 1.23+，MyGo（原生模式；`internal/ui/webview` 作为页面级回退）。纯 Go，无 cgo。
- dsh 运行时：Node 24 LTS 便携版 + `@deepseek-ai/dsh`（锁版本）+ `dsh-base` + `dsh-sdk-app`，随包。
- 构建：GoReleaser 或自写脚本产出 Windows x64 安装包（MSIX 或 NSIS，验证周定）；MyGo 的签名增量更新。
- 存储：`%LOCALAPPDATA%\Alcove\`：`identity.json`、`workspaces.json`、`settings.json`、`plugins\`、`logs\`；dsh home 在 `%LOCALAPPDATA%\Alcove\dsh\`，每工作区 profile 在 `dsh\profiles\ws-<id>\`。
- 凭据：Windows Credential Manager（`wincred`），只存 key；其余配置明文 JSON。

## 2. 模块

```
cmd/alcove                 入口、单实例锁、崩溃上报到本地日志
internal/ui                MyGo 视图：工作区列表、对话视图、审批卡、提问表单、设置、插件页、运行时面板
internal/ui/declarative    声明式 UI 渲染器：settings_page / sidebar_item / card 三类 schema → MyGo 控件
internal/core/identity     本地身份、凭据引用、钥匙串读写
internal/core/workspace    工作区 CRUD、目录校验、每工作区 profile 名
internal/runtime           dsh 进程管理：拉起、就绪探测、JSON-RPC 客户端、空闲计时、停止；一个工作区一个进程
internal/runtime/events    dsh 事件 → UI 模型（消息、工具调用、审批、提问、子代理、完成/失败）
internal/profile           profile 目录生成、bundle add/remove（npm/.tgz）、--dump-config 校验、按归属组装（首期只有个人）
internal/plugins/host      插件生命周期：安装/卸载、manifest、签名、子进程启动、JSON-RPC、崩溃限频
internal/plugins/caps      能力授权：settings_page, workspace_hook, runtime_launch_hook, provider_endpoint, background, inbox, deep_link
internal/plugins/api       宿主对插件暴露的 RPC：workspace.list/get、runtime.launchHook 注册、ui.register、settings.read/write、keychain.ref（只给引用不给值）、log
internal/policy            策略快照读取（由插件写入 settings 的受保护段）：executors.local.enabled、update.source、plugins.allowed/required/require_signature
internal/update            签名增量更新客户端；更新源可配置
internal/diag              诊断包导出与脱敏
installer/                 安装包脚本、签名流水线
e2e/                       端到端脚本与报告模板
```

## 3. 关键接口

### 3.1 dsh 运行时

- 启动：`node dsh.js --profile <ws-profile> --dsh-home <home>`，cwd = 工作区目录，env 注入 `DEEPSEEK_API_KEY`/`DEEPSEEK_BASE_URL`（或 provider 对应变量），由 `runtime_launch_hook` 允许插件追加 env 与 profile patch。
- 就绪：JSON-RPC `initialize` 成功；超时 15 s 判失败并展示 stderr 尾部。
- 空闲：最后一个会话事件后计时，默认 600 s；钉住则不退。
- 退出：先 `shutdown` RPC，5 s 后强杀；记录会话 JSONL 是否完整（文件尾校验）。

### 3.2 插件协议 v1（JSON-RPC 2.0 over stdio）

```
握手   host → plugin: hello{ protocol: 1, alcoveVersion, grantedCaps[] }
       plugin → host: ready{ name, version, uiContributions[] }
宿主提供（plugin 可调）
  workspace.list() / workspace.get(id)
  runtime.registerLaunchHook()            需要 runtime_launch_hook；之后宿主在每次拉起前调 plugin.launchHook(ctx) 取 env/profilePatch
  ui.register(contribution)               需要 settings_page 等能力；contribution 为声明式 schema
  ui.update(id, data)                      刷新卡片/侧栏数据
  settings.read(ns) / settings.write(ns, v) 命名空间隔离，策略段只有带 policy_write 能力的插件可写
  keychain.ref(name) / keychain.resolveForRuntime(ref)  值只在拉起 dsh 时由宿主注入，插件拿不到明文
  inbox.post(item)                         需要 inbox；向通用对话视图投递卡片
  log(level, msg)
插件提供（宿主可调）
  launchHook(ctx) → { env, profilePatch }
  onWorkspaceEvent(evt)                    需要 workspace_hook：opened/closed/created/removed
  onUiAction(contributionId, action, payload)
  onDeepLink(url)                          需要 deep_link
  shutdown()
```

- manifest：`name, version, hosts.alcove（SemVer 范围）, requires[], capabilities[], dsh_bundles[]{name, version, profiles[]}, ui[], exec{ windows: "bin/plugin.exe" }, signature`。
- 签名：Ed25519，公钥随 Alcove 内置，可由策略追加组织公钥。
- 声明式 UI schema v1：`settings_page{ sections[]{ fields[]: text|secretRef|toggle|select|button } }`、`sidebar_item{ title, icon, viewId }`、`card{ title, body(markdown 子集), actions[]{ id, label, style } }`。

### 3.3 profile 组装（首期）

个人工作区 profile = `dsh-base` + 用户添加的 bundle + 已启用插件 manifest 里声明给该归属的 bundle。写入 `dsh.profile.bundles` 后执行 `--dump-config`，失败则回退上一版 profile 并提示。

## 4. 里程碑与任务

- M0（验证周，已单列）。
- M1 骨架（2 周）：MyGo 窗口 + 工作区列表 + 拉起 dsh + 对话视图渲染消息与工具调用；凭据管理；空闲退出。验收 A1 的前半段。
- M2 完整对话（2 周）：审批卡、提问表单、子代理、会话列表与续跑、模型/思考强度选择；运行时面板；诊断包。验收 A1、A4。
- M3 profile 与 bundle（1 周）：npm/.tgz 添加、校验、回退。验收 A2。
- M4 插件宿主（3 周）：协议 v1、能力授权、声明式 UI、签名、崩溃限频、卸载；策略段读取；提供 Go 空插件样例与 SDK（`alcove-plugin-go` 小包，仅 RPC 封装）。验收 A3。
- M5 安装与更新（1 周）：完整构建打包、签名、增量更新、更新源配置。验收 A5。
- 合计约 9 周，单人全栈节奏；并行两人可压到 6 周（UI 与 runtime/plugins 分开）。

## 5. 测试策略

只做端到端：`e2e/` 下用 Go 驱动真实安装包与真实 dsh（模型端点用本地假服务返回固定回复与工具调用），覆盖 A1–A5，每次产出 Markdown 报告与截图到 `e2e/report/`，报告即验收工件。不写单元测试。

## 6. 风险与对策

- MyGo 原生模式成熟度：按验证周结论决定页面级 webview 回退范围。
- dsh rc 版本破坏性变更：锁版本；升级只在发版窗口，并重跑 e2e。
- 声明式 UI 表达力不够：首期只承诺三类贡献，插件更复杂的视图走通用对话视图的卡片。
- Node 便携版的杀毒软件误报：安装包整体签名，首批客户终端加白名单，文档写明。
