# Alcove 首期执行 Spec

版本 0.2 · 对应 PRD 0.2（macOS 纳入首期）

## 1. 技术栈

- Go 1.27+（MyGo v0.7.x 的要求），MyGo（原生模式；`internal/ui/webview` 作为页面级回退）。纯 Go，无 cgo。
- dsh 运行时：Node 24 LTS 便携版 + `@deepseek-ai/dsh`（锁版本）+ `dsh-base` + `dsh-sdk-app` + pnpm（`dsh plugin` 调用 PATH 上的 `pnpm`，Alcove 拉起 dsh 时把随包 pnpm 放进 PATH），随包。
- 平台：Windows 10+ x64；macOS 13+，arm64 与 amd64 合成通用二进制（`lipo`）。CI 每个 PR 交叉编译两个平台；macOS e2e 不进每次 CI（macOS runner 分钟数贵 10 倍），在 Mac 基线机上发版前跑。
- 构建：GoReleaser 或自写脚本。Windows x64 安装包（MSIX 或 NSIS，验证周定）。macOS：`Alcove.app`（`Contents/Resources/` 下放对应平台的 Node、dsh 与 pnpm），`codesign` 用 Developer ID、开 hardened runtime，`notarytool` 公证并 staple，装进 `.dmg`。随包 Node 需要 `com.apple.security.cs.allow-jit` 与 `allow-unsigned-executable-memory` 两项 entitlement，否则 V8 起不来。MyGo 的签名增量更新两平台通用。
- 存储（下文的 `<数据目录>`）：Windows `%LOCALAPPDATA%\Alcove\`，macOS `~/Library/Application Support/Alcove/`。其下 `identity.json`、`workspaces.json`、`settings.json`、`plugins/`、`logs/`；dsh home 在 `<数据目录>/dsh/`，每工作区 profile 在 `dsh/profiles/ws-<id>/`。
- 本机 IPC 端点（桥接、daily-tools 宿主等回连）：Windows 命名管道；macOS 用 Unix 套接字，放在 `$TMPDIR/alcove-<短随机名>.sock`，不放 `<数据目录>`（macOS 的套接字路径上限 104 字节，`~/Library/Application Support/...` 容易超）。
- 凭据：经 MyGo `Secrets` 存系统凭据库：Windows Credential Manager，macOS 登录钥匙串（服务名 `Alcove`）。只存 key；其余配置明文 JSON。

## 2. 模块

```
cmd/alcove                 入口、单实例锁、崩溃上报到本地日志
internal/ui                MyGo 视图：工作区列表、对话视图、审批卡、提问表单、设置、插件页、运行时面板
internal/ui/declarative    声明式 UI 渲染器：settings_page / sidebar_item / card 三类 schema → MyGo 控件
internal/core/identity     本地身份、凭据引用、钥匙串读写
internal/core/workspace    工作区 CRUD、目录校验、每工作区 profile 名
internal/core/credentials  凭据记录（credentials.json，不含 key）+ 系统凭据库里的 key；拉起时 LaunchEnv 生成 provider 的环境变量
internal/core/paths        各平台数据目录与文件布局（ALCOVE_DATA_DIR 可覆盖）
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
- 审批与提问：`dsh-sdk-app`（rc.5）没有审批通道，审批请求直接判 `unavailable`（验证报告第 4 节）。Alcove 随包带第一方 bundle `@precinct/alcove-bridge`（`bridge/`），装进每个工作区 profile。它的 patch 关掉 `session-log-deepseek`，补上 sdk 模板缺的 `ask_user_question` 工具（`@deepseek-ai/dsh-tool-ask-user`），并挂 `approval/request` 与 `user-questions/request` 两个终端监听器，经本机 IPC 回连 Alcove。连接约定同 Charter protocols.md 的宿主回连约定：`ALCOVE_BRIDGE_HOST` / `ALCOVE_BRIDGE_HOST_TOKEN`，令牌每次拉起一个、一次性，握手后从进程环境删除。协议 `alcove-bridge/1`，每行一个 JSON-RPC 帧：
  - bridge → 宿主 `host.hello { protocol, token, pid }`
  - bridge → 宿主 `approval.ask { requestId, sessionId, toolName, callId?, reason, displayReason? }` → `{ outcome: "allowed-once" | "rejected" | "cancelled" }`。`displayReason` 是 dsh 多语言文本（字符串或 `{ en, zh }`），界面按语言取，`reason` 是给模型看的原文。
  - bridge → 宿主 `questions.ask { requestId, sessionId, callId?, questions[] }` → `{ answers: [{ id, selected[], custom? }] }`
  - bridge → 宿主通知 `request.cancel { requestId }`：dsh 撤回了请求（回合取消等），界面关掉对应卡片。连接断开时宿主同样关掉该运行时的所有卡片。
  - 宿主不可达或握手失败时监听器交给下一个（即 dsh 默认的 `unavailable`），不放行。
  - 每个审批结果与回答追加一行到 `<数据目录>/logs/decisions.jsonl`（PRD F3）。
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
    sidecar: { windows: "bin/<name>.exe", darwin: "bin/<name>" }  可选；键是 GOOS，macOS 边车须为 arm64+amd64 通用二进制并签名
    ui: [...]                             声明式 UI schema v1
```

- 首期第一方包不带 install/postinstall 脚本；Alcove 调用 dsh 时不批准任何构建脚本。
- 签名：Ed25519，公钥随 Alcove 内置，可由策略追加组织公钥。dsh/pnpm 不验发布者签名，签名只由 Alcove 校验。

**安装流程（F5）**

1. 选择 `.tgz`（同目录需有 `.sig`）→ 先复制一份快照到 `plugins\staging\`，对快照算摘要、按摘要校验签名，之后的检查与安装都只用这份快照，源文件再改也不影响。
2. 读包内 `package.json`：没有 `dsh.bundle.patch` 即拒绝（dsh CLI 对这种包不回滚，只当普通依赖装上）；`engines.dsh` 不覆盖随包 dsh 版本即拒绝（不走 dsh 的版本豁免；dsh 安装时会再判一次，以 dsh 为准）；`alcove.host` 不覆盖当前 Alcove 版本即拒绝；带 install 类脚本即拒绝；`alcove.requires` 未满足即提示先装依赖；同名包已安装（含上次失败留下的未完成记录）即提示先卸载；包仓或目标 profile 里已有用户手工装的同名包即拒绝（不接管，回滚也不会动它）。
3. 展示能力清单，用户确认。
4. 确认后在锁内重查已装记录、依赖与 profile 冲突；把快照复制到 `<数据目录>/plugins/packages/<name>-<version>-<摘要前缀>.tgz` 并核对摘要，后续一律用这份副本安装（profile 记录的是 `.tgz` 绝对路径，原文件被挪走后该 profile 的后续安装都会失败），卸载时才删除。
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
  runtime.ensure(profile, cwd, env?) → { runtimeId }  需要 background；按需拉起指定 profile 的 dsh（daily 回合进程用）。只允许插件自己装入的非工作区 profile（如 daily），cwd 须为已存在的绝对目录；env 是插件给的变量（如 DAILY_TOOLS_HOST/_TOKEN），保留名（ALCOVE_*、DSH_HOME）被拒（-32602）；凭据由宿主注入。卸载插件时一并停掉这些运行时。
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

**边车运行（实现：`internal/plugins/sidecar`；e2e：`e2e/sidecar_test.go`）**

- 拉起：可执行文件取包仓 profile `node_modules/<name>/` 下 `alcove.sidecar[<GOOS>]`，工作目录为其所在目录，独立进程组（Windows 按进程树结束，macOS 按进程组结束）。包里缺当前平台的边车即安装被拒（`sidecar` 问题）。pnpm 解包的文件不带 `com.apple.quarantine`，不触发 Gatekeeper；第一方边车仍用 Developer ID 签名，社区包至少要 ad-hoc 签名（Apple Silicon 不跑未签名代码，Go 链接器默认会 ad-hoc 签）。
- 握手：宿主发请求 `hello{ protocol, alcoveVersion, dshVersion, grantedCaps[] }`，插件以结果 `ready{ name, version, uiContributions[] }` 应答；10 秒内未应答、`name` 与包名不符、或 ready 里的界面贡献未获授权，都算握手失败。
- 把关：`runtime.registerLaunchHook` 需 `runtime_launch_hook`，`runtime.ensure` 需 `background`，`inbox.post` 需 `inbox`；界面贡献 `settings_page`、`sidebar_item` 需同名能力，`card` 需 `inbox`；`ui.update` 只能改自己登记过的贡献。设置命名空间：插件拥有与包名同名的命名空间及其下 `<name>/...`；`policy` 段人人可读，只有 `policy_write` 可写；其他插件的命名空间一律拒绝。
- 错误码：`-32030` 能力或命名空间未授权；`-32031` 宿主暂未提供该服务（如 `keychain.resolveForRuntime`，随凭据 PR 提供）；`-32602` 参数错误；`-32601` 未知方法。`log` 既可作请求也可作通知。
- 拉起钩子：每次拉起 dsh 前按插件名顺序调用已登记插件的 `launchHook(ctx{ workspaceId, profile, cwd })`，每个限时 3 秒；失败、超时或想设置保留变量（`ALCOVE_*`、`DSH_HOME`）的钩子被跳过并记日志，不阻塞拉起。
- 停止：`shutdown` 请求后等 3 秒，再结束整个进程树；插件登记的界面贡献与拉起钩子随之撤销。
- 崩溃限频：未经宿主要求的退出（含握手失败）算一次崩溃，按 1 s、2 s、4 s… 退避重启；10 分钟内满 3 次即停在 `crashed`，插件页显示最后错误与 stderr 尾部，用户手动启动时清零计数。

### 3.3 profile 组装（首期）

个人工作区 profile = sdk 模板（`--from-default-profile sdk`，含 `dsh-base` 与 `dsh-sdk-app`）+ `@precinct/alcove-bridge` + 用户添加的 bundle + `alcove.profiles` 含 `personal` 的已启用插件包。插件声明的其它 profile（如 `daily`）不存在时按同样的 sdk 模板 + 桥接先建好再装插件。新建工作区时把已装的 personal 插件补进去；移除工作区时删 profile 并从插件记录里去掉。实现：`internal/profile`、`internal/app`；e2e：`e2e/app_test.go`。全部经 `dsh plugin` 写入 `dsh.profile.bundles`，之后执行 `--dump-config`，失败则回退上一版 profile 并提示。Alcove 不直接改写 profile 的 `package.json` 或 `node_modules`。

## 4. 里程碑与任务

- M0（验证周，已单列）。macOS 补测：在 Mac 基线机重跑验证周第 1、2 项（MyGo 冷启动与内存、中文输入法、dsh 安装与插件链路、SDK 回合），并核实 dsh 在 macOS 上的沙盒行为。
- M1 骨架（2 周）：MyGo 窗口 + 工作区列表 + 拉起 dsh + 对话视图渲染消息与工具调用；凭据管理；空闲退出。验收 A1 的前半段。
- M2 完整对话（2 周）：审批卡、提问表单、子代理、会话列表与续跑、模型/思考强度选择；运行时面板；诊断包。验收 A1、A4。
- M3 profile 与 bundle（1 周）：npm/.tgz 添加、校验、回退。验收 A2。
- M4 插件宿主（3 周）：dsh 插件包安装链路、宿主扩展协议 v1、能力授权、声明式 UI、签名、崩溃限频、卸载；策略段读取；提供最小样例包（空组合包 patch + Go 边车）与 SDK（`alcove-plugin-go` 小包，仅 RPC 封装）。验收 A3。
- M5 安装与更新（2 周）：两平台完整构建打包、签名、macOS 公证、增量更新、更新源配置。验收 A5。
- 各里程碑的验收在 Windows 与 Mac 两台基线机上都跑，平台差异（凭据库、数据目录、进程树、输入法）随所在里程碑做，不单列。
- 合计约 10.5 周，单人全栈节奏；并行两人可压到 7 周（UI 与 runtime/plugins 分开）。

## 5. 测试策略

只做端到端：`e2e/` 下用 Go 驱动真实安装包与真实 dsh（模型端点用本地假服务返回固定回复与工具调用），覆盖 A1–A5，每次产出 Markdown 报告与截图到 `e2e/report/`，报告即验收工件。不写单元测试。

## 6. 风险与对策

- MyGo 原生模式成熟度：按验证周结论决定页面级 webview 回退范围。
- dsh rc 版本破坏性变更：锁版本；升级只在发版窗口，并重跑 e2e。插件包的兼容性统一由 `engines.dsh` 兜底，升级 dsh 时先跑 Dovetail 全部包的安装校验。
- 声明式 UI 表达力不够：首期只承诺三类贡献，插件更复杂的视图走通用对话视图的卡片。
- Node 便携版的杀毒软件误报：安装包整体签名，首批客户终端加白名单，文档写明。
- macOS 公证与 hardened runtime：随包 Node 缺 JIT entitlement 会直接崩；dsh 与 pnpm 在包内写文件会破坏签名，所以 dsh home 与 profile 一律放 `<数据目录>`，`.app` 内只读。发版流水线需要 Apple Developer ID 证书与公证账号。
- macOS 上 dsh 的沙盒实现与 Windows 不同（验证周补测），审批与提权行为以实测为准，e2e 的提权用例按平台写。
