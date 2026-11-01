# Alcove 首期执行 Spec

版本 0.1 · 对应 PRD 0.1

## 1. 技术栈

- Go 1.27+（MyGo v0.7.x 的要求），MyGo（原生模式；`internal/ui/webview` 作为页面级回退）。纯 Go，无 cgo。
- dsh 运行时：Node 24 LTS 便携版 + `@deepseek-ai/dsh`（锁版本）+ `dsh-base` + `dsh-sdk-app` + pnpm（`dsh plugin` 调用 PATH 上的 `pnpm`，Alcove 拉起 dsh 时把随包 pnpm 放进 PATH），随包。
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
internal/plugins/host      插件生命周期：签名校验、读 package.json 的 engines.dsh 与 alcove 字段、经 dsh plugin add/remove 安装卸载、边车子进程启动、JSON-RPC、崩溃限频
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
- 审批与提问：`dsh-sdk-app`（rc.5）没有审批通道，审批请求直接判 `unavailable`（验证报告第 4 节）。Alcove 随包带第一方 bundle `@precinct/alcove-bridge`，装进每个工作区 profile：挂 `approval/request` 终端监听器与 `ctx.userQuestions` 回答方，经本机 IPC（Windows 命名管道；地址与一次性令牌经环境变量注入，约定同 Charter protocols.md 的宿主回调约定）回连 Alcove；同一 patch 关掉 `session-log-deepseek`。
- 模型端点：dsh 自带 DeepSeek 适配器讲 Anthropic Messages API。指向 OpenAI 兼容端点（含本机 Gatehouse，`http://127.0.0.1:7788/v1`）时，在 profile patch 里配 `@deepseek-ai/dsh-llm-pi-ai` 的 `openai-completions` 路由，静态请求头 `X-Gatehouse-Source: alcove`。
- 空闲：最后一个会话事件后计时，默认 600 s；钉住则不退。
- 退出：先 `shutdown` RPC，5 s 后强杀；记录会话 JSONL 是否完整（文件尾校验）。

### 3.2 插件包与宿主扩展协议 v1

插件包遵循 dsh 官方插件协议（Charter ADR 0001）：每个 Alcove 插件都是一个标准 dsh 插件 npm 包，宿主侧能力另在 `package.json` 的 `alcove` 字段声明。

**包格式**

```
<name>-<version>.tgz          标准 npm 包，可直接 dsh plugin add
<name>-<version>.tgz.sig      Ed25519 分离签名：一行标准 base64，内容为对 .tgz 的 32 字节 SHA-256 摘要的 64 字节签名
package.json
  name, version, description
  engines.dsh                 dsh 原生兼容校验，与随包 dsh 版本比对
  dsh.bundle.patch            组合包 patch 路径（验证周核实）；无运行时内能力也声明空 patch `[]`
  dependencies                其它 dsh 包（如 daily-tools），pnpm 解析
  alcove:
    protocol: 1
    host: "<Alcove SemVer 范围>"
    requires: ["<插件名>@<范围>"]          宿主侧依赖，安装前检查
    capabilities: [...]                   见 internal/plugins/caps
    profiles: ["personal" | "daily" | "worker" | ...]   装入哪些 profile；personal 指所有个人工作区
    sidecar: { windows: "bin/<name>.exe" }  可选
    ui: [...]                             声明式 UI schema v1
```

- 首期第一方包不带 install/postinstall 脚本；Alcove 调用 dsh 时不批准任何构建脚本。
- 签名：Ed25519，公钥随 Alcove 内置，可由策略追加组织公钥。dsh/pnpm 不验发布者签名，签名只由 Alcove 校验。

**安装流程（F5）**

1. 选择 `.tgz`（同目录需有 `.sig`）→ 先复制一份快照到 `plugins\staging\`，对快照算摘要、按摘要校验签名，之后的检查与安装都只用这份快照，源文件再改也不影响。
2. 读包内 `package.json`：没有 `dsh.bundle.patch` 即拒绝（dsh CLI 对这种包不回滚，只当普通依赖装上）；`engines.dsh` 不覆盖随包 dsh 版本即拒绝（不走 dsh 的版本豁免；dsh 安装时会再判一次，以 dsh 为准）；`alcove.host` 不覆盖当前 Alcove 版本即拒绝；带 install 类脚本即拒绝；`alcove.requires` 未满足即提示先装依赖；同名包已安装（含上次失败留下的未完成记录）即提示先卸载；包仓或目标 profile 里已有用户手工装的同名包即拒绝（不接管，回滚也不会动它）。
3. 展示能力清单，用户确认。
4. 确认后在锁内重查已装记录、依赖与 profile 冲突；把快照复制到 `%LOCALAPPDATA%\Alcove\plugins\packages\<name>-<version>-<摘要前缀>.tgz` 并核对摘要，后续一律用这份副本安装（profile 记录的是 `.tgz` 绝对路径，原文件被挪走后该 profile 的后续安装都会失败），卸载时才删除。
5. `dsh plugin --profile alcove-plugins add <副本>`：保留 profile，只作包仓，永不启动；边车从这里的 `node_modules/<name>/` 拉起。
6. 对 `alcove.profiles` 覆盖的每个 profile 执行 `dsh plugin --profile <p> add <副本>`，确认已选入 `dsh.profile.bundles`，再 `--dump-config` 校验。
7. 任一步失败：已动过的 profile（含失败的那个）逐个 `dsh plugin remove`，包仓同样移除，删除副本，界面展示 dsh 诊断原文。回滚本身失败时保留副本，写一条 `incomplete` 记录，列表可见，可经卸载重试清理；未完成记录不满足其他包的 `requires`。dsh 子进程超时按进程树结束。
8. 卸载顺序相反：停边车 → 记录先标 `incomplete` → 各 profile remove → 包仓 remove → 删除副本 → 删记录。中途失败则留下 `incomplete` 记录；profile 清单读不出不算已移除。pnpm 留下的空作用域目录一并删除。
9. 安装记录在 `plugins\installed.json`。实现：`internal/plugins/host`、`internal/plugins/pkg`、`internal/plugins/sig`、`internal/dsh`；e2e：`e2e/plugininstall_test.go`。

**宿主扩展协议 v1（边车，JSON-RPC 2.0 over stdio）**

```
握手   host → plugin: hello{ protocol: 1, alcoveVersion, dshVersion, grantedCaps[] }
       plugin → host: ready{ name, version, uiContributions[] }
宿主提供（plugin 可调）
  workspace.list() / workspace.get(id)
  runtime.registerLaunchHook()            需要 runtime_launch_hook；之后宿主在每次拉起前调 plugin.launchHook(ctx) 取 env/profilePatch
  runtime.ensure(profile, cwd)            需要 background；按需拉起指定 profile 的 dsh（daily 回合进程用）
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

- 没有 `alcove.sidecar` 的插件只有运行时内能力与声明式 UI，不起子进程。
- 声明式 UI schema v1：`settings_page{ sections[]{ fields[]: text|secretRef|toggle|select|button } }`、`sidebar_item{ title, icon, viewId }`、`card{ title, body(markdown 子集), actions[]{ id, label, style } }`。

### 3.3 profile 组装（首期）

个人工作区 profile = `dsh-base` + 用户添加的 bundle + `alcove.profiles` 含 `personal` 的已启用插件包。全部经 `dsh plugin` 写入 `dsh.profile.bundles`，之后执行 `--dump-config`，失败则回退上一版 profile 并提示。Alcove 不直接改写 profile 的 `package.json` 或 `node_modules`。

## 4. 里程碑与任务

- M0（验证周，已单列）。
- M1 骨架（2 周）：MyGo 窗口 + 工作区列表 + 拉起 dsh + 对话视图渲染消息与工具调用；凭据管理；空闲退出。验收 A1 的前半段。
- M2 完整对话（2 周）：审批卡、提问表单、子代理、会话列表与续跑、模型/思考强度选择；运行时面板；诊断包。验收 A1、A4。
- M3 profile 与 bundle（1 周）：npm/.tgz 添加、校验、回退。验收 A2。
- M4 插件宿主（3 周）：dsh 插件包安装链路、宿主扩展协议 v1、能力授权、声明式 UI、签名、崩溃限频、卸载；策略段读取；提供最小样例包（空组合包 patch + Go 边车）与 SDK（`alcove-plugin-go` 小包，仅 RPC 封装）。验收 A3。
- M5 安装与更新（1 周）：完整构建打包、签名、增量更新、更新源配置。验收 A5。
- 合计约 9 周，单人全栈节奏；并行两人可压到 6 周（UI 与 runtime/plugins 分开）。

## 5. 测试策略

只做端到端：`e2e/` 下用 Go 驱动真实安装包与真实 dsh（模型端点用本地假服务返回固定回复与工具调用），覆盖 A1–A5，每次产出 Markdown 报告与截图到 `e2e/report/`，报告即验收工件。不写单元测试。

## 6. 风险与对策

- MyGo 原生模式成熟度：按验证周结论决定页面级 webview 回退范围。
- dsh rc 版本破坏性变更：锁版本；升级只在发版窗口，并重跑 e2e。插件包的兼容性统一由 `engines.dsh` 兜底，升级 dsh 时先跑 Dovetail 全部包的安装校验。
- 声明式 UI 表达力不够：首期只承诺三类贡献，插件更复杂的视图走通用对话视图的卡片。
- Node 便携版的杀毒软件误报：安装包整体签名，首批客户终端加白名单，文档写明。
