# Alcove 首期 PRD

版本 0.2 · 对应架构文档 v0.18 · 范围：首期（MVP）

0.2 变更（2026-11-01，liuliu）：macOS 纳入首期，与 Windows 同等验收。

0.3 变更（2026-11-02，liuliu）：首期不开自动更新，发布位置以后再定。首期安装包不配更新源，应用内更新代码已在（设置页"更新"），配置 `updates` 后即生效；A5 改为安装包验收，增量更新验收顺延。

## 1. 目标

交付一个裸的 dsh 桌面客户端：安装即用、零服务端、完整 dsh 能力，并提供插件宿主，使日常 bot 与组织功能可以在不重编译的前提下由插件加上。首期衡量标准只有两条：低配 Windows 机器与 Mac 上都可用，裸装不含任何业务功能。

## 2. 用户与场景

- 个人用户：有自己的模型 API，想在本机用 dsh 做工作任务，不想碰命令行。
- 组织成员（装 `cloister` 插件后）：同一个客户端，加入组织后多出组织工作区与云端能力。首期 Alcove 自身只保证插件宿主能力到位，组织功能的体验由 Dovetail 的插件负责。
- 终端管理员：需要一个签名安装包（Windows）或签名并公证的应用（macOS）、可控的更新源、可禁用本地执行的策略开关。

## 3. 范围

首期内：

1. 安装与更新：Windows x64 签名安装包；macOS 通用二进制（Apple Silicon 与 Intel）的 `.app`，Developer ID 签名并经 Apple 公证，以 `.dmg` 分发。两者都是完整构建（含对应平台的 Node 与 dsh-base）。签名增量更新与可配置的更新源已实现，首期发布不启用（0.3）。
2. 个人身份：首次启动生成本地身份；API 凭据录入与管理（provider、base_url、key），key 存系统凭据库（Windows 凭据管理器、macOS 登录钥匙串），仅在拉起 dsh 时注入进程。
3. 工作区：新建、打开、重命名、移除（不删文件）；每个工作区一个目录；首期全部个人归属。
4. 对话与任务：通用 dsh 事件视图：消息、工具调用摘要与展开、审批卡（允许/拒绝/总是允许）、提问表单、子代理进度、会话列表与续跑、按会话选模型与思考强度；非流式整条消息。
5. 运行时生命周期：按需拉起、空闲退出（默认 10 分钟，可钉住）、运行中列表、手动停止；一个工作区一个进程。
6. profile 与 dsh bundle 管理：每工作区 profile；从 npm registry 或本地 .tgz 添加 bundle；`--dump-config` 校验；错误可读。
7. 插件宿主：从文件安装插件包（插件包即标准 dsh 插件 npm 包，经 `dsh plugin add` 安装，见 Charter ADR 0001）；签名、`engines.dsh` 与 `alcove` 字段校验；边车子进程 JSON-RPC；能力授权并在安装时展示；声明式 UI 渲染（设置页、侧栏项、卡片模板）；崩溃重启限频；卸载。
8. 诊断：一键导出脱敏日志包。
9. 策略开关（供 `cloister` 插件写入，Alcove 读取）：`executors.local.enabled`、更新源、插件白名单与强制列表、签名要求。

首期外：日常 bot、记忆、日程（`daily` 插件）；组织功能（`cloister` 插件）；市场视图（`dovetail` 插件）；Linux；多语言 UI；自绘插件视图。

## 4. 功能需求

- F1 Windows 安装包双击安装，无管理员权限也能装到用户目录；macOS 从 `.dmg` 拖入「应用程序」或 `~/Applications` 即可，无需管理员，首次打开 Gatekeeper 不报警。首次启动 ≤ 2 s 到可交互（基线机）。
- F2 凭据不落盘明文；导出日志包不含 key；dsh 进程退出后 key 不残留在任何文件。
- F3 对话视图对 dsh 的审批与提问事件做到"不丢、不重复、可追溯"，每个审批决定有本地记录。
- F4 运行时异常退出时，会话 JSONL 不损坏，重开工作区可续跑。
- F5 插件安装流程：选择文件 → 校验签名、`engines.dsh` 与 `alcove.host` 版本范围 → 展示能力清单 → 用户确认 → 经 `dsh plugin add` 装入包仓与 `alcove.profiles` 列出的 profile → `--dump-config` 校验。任一步失败回滚。同一个包不经 Alcove、直接用 `dsh plugin add` 也能装进任意 dsh profile（只缺宿主侧能力）。
- F6 插件崩溃 3 次/分钟自动停用并提示。
- F7 `executors.local.enabled=false` 时，拉起运行时的所有入口不可用并说明原因，运行时文件保留。

## 5. 非功能需求

- 基线机：Windows（4 核 8 GB 集成显卡 Win10）与 Mac（Apple Silicon 8 GB，macOS 13+，先以用户的 Mac mini 为准；Intel Mac 跑通 A1 即可）。Alcove 空闲内存 ≤ 60 MB（口径为进程自身占用：macOS 的 physical footprint，即活动监视器“内存”列；Windows 的专用工作集，即任务管理器“内存”列。不用 RSS，因为它主要是各应用共用的系统库；不含打开工作区后的 dsh 运行时）；dsh 冷启动 ≤ 3 s；安装包 ≤ 400 MB（macOS 通用包另计，≤ 500 MB）。数字以验证周结论为准。
- 中文输入法（Windows 微软拼音、macOS 系统拼音）无阻断缺陷；Windows VDI 下可用（原生或 webview 回退）。
- 无任何对外网络请求，除用户配置的模型端点与更新源。
- 签名：安装包、更新包、插件包三类签名链独立可轮换。

## 6. 依赖与接口

- dsh：`@deepseek-ai/dsh` 锁版本，`dsh-sdk-app` JSON-RPC；`sdk-minimal` 沙盒为 `danger-full-access`，Alcove 不另做隔离，文档如实说明。
- MyGo：原生模式为主（Windows 与 macOS 均 GPU 绘制，macOS 走 Metal），页面级 webview 回退（WebView2 / WKWebView）。
- macOS 分发依赖 Apple Developer ID 证书与公证账号（终端用户无感，发版流水线需要）。
- 对插件：包格式遵循 dsh 官方插件协议；宿主侧能力走 Alcove 宿主扩展协议 v1（见 SPEC 3.2）。
- 对模型：OpenAI 兼容端点；可指向本机 Gatehouse。

## 7. 验收标准（端到端）

脚本化、可重复，产出 `e2e/report/*.md` 与截图。A1–A5 在 Windows 基线机与 Mac 基线机上各跑一遍，报告分平台：

- A1 干净的基线机安装 → 配置一个 API → 新建工作区 → 发起一个含文件写入与审批的任务 → 审批 → 完成 → 10 分钟后运行时自动退出；全程抓包只有模型端点流量。
- A2 添加一个社区 dsh bundle（.tgz）到个人工作区 → `--dump-config` 通过 → 下次拉起可见其命令。
- A3 安装一个测试插件包（空组合包 patch + Go 边车，Windows 与 macOS 各一份，带设置页声明）→ 包出现在目标 profile 的 `dsh.profile.bundles` 且 `--dump-config` 通过 → 设置页可见 → 让边车崩溃 3 次 → 自动停用 → 卸载后各 profile 与包仓都无残留；另把 `engines.dsh` 改成不兼容范围重打包 → 安装被拒。
- A4 导出诊断包 → 用脚本扫描确认不含 key 字样与凭据。
- A5 安装包：从安装包（Windows 安装程序 / macOS `.dmg`）装到干净的基线机 → 启动 → 随包运行时（node、dsh、pnpm）可用 → `alcove://` 链接交给已开的实例 → 卸载后不留程序文件（数据目录保留）。增量更新（N 版升到 N+1 版，运行时同步变化，工作区与设置保留）顺延到开启自动更新时。
