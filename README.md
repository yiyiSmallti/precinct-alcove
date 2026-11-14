# Alcove

Precinct 系列 · 基于 dsh（DeepSeek Harness）的轻量桌面客户端 + 插件宿主。

## 定位

一个人自己的角落：常驻的 Go 进程（MyGo 原生 UI + 客户端核心）、按需拉起的完整 dsh 运行时、按需拉起的插件子进程。安装包为完整构建，运行时随包签名发布，与客户端同版本号。

**裸装只有 dsh 基础功能**：个人身份与 API 凭据（钥匙串，注入 dsh 进程）、工作区、对话与本机任务执行（dsh 完整能力：工具、审批、提问表单、子代理、skills、MCP）、运行时生命周期、每工作区 profile 的 dsh bundle 管理（dsh 原生机制，用户可装任意社区 bundle）、只有"从文件安装插件包"一个入口的插件宿主。没有日常 bot、记忆、日程、组织功能，也没有市场。不依赖任何服务端，模型调用直连 OpenAI 兼容端点；需要路由或脱敏时可指向单独运行的本机 [Gatehouse](https://github.com/yiyiSmallti/precinct-gatehouse)。

其余一切都是 Alcove 插件，由 [Dovetail](https://github.com/yiyiSmallti/precinct-dovetail) 提供（只上第一方插件）：

- `dovetail`：插件市场本身。随安装包预装、可卸载；没有它宿主仍能从文件安装。

- `daily`：本机日常 bot（对话视图、记忆、日程提醒、信箱）。装上它就是个人版。目前随安装包预装、可卸载。
- `cloister`：组织功能（加入组织、工作区归属、同步与租约、主力环境切换、云端任务），依赖 `daily`。装上它就是组织版。目前随安装包预装、可卸载；没加入组织时只是闲着。

## 插件机制

- 插件包就是标准 dsh 插件 npm 包（`.tgz` + Ed25519 签名），遵循 dsh 官方插件协议：`engines.dsh` 版本范围、组合包 patch，经 `dsh plugin add` 安装（[ADR 0001](https://github.com/yiyiSmallti/precinct-charter/blob/main/docs/adr/0001-plugin-packages-are-dsh-packages.md)）。
- 宿主侧能力在 `package.json` 的 `alcove` 字段声明：Alcove 版本范围、能力（设置页、工作区钩子、运行时启动钩子、provider 端点、后台、信箱、深链）、目标 profile、声明式 UI、可选边车可执行文件。
- 边车是子进程，JSON-RPC over stdio；宿主握手校验协议版本，按声明的能力授权。
- UI 为声明式贡献（设置页、侧栏项、卡片模板），由 Alcove 用 MyGo 渲染，插件不画像素。
- 边车的参考模式：hashicorp/go-plugin（子进程 + RPC、握手、版本协商），改用 JSON-RPC over stdio 与 dsh 运行时一致。

两层插件：Alcove 插件（子进程，第一方）管宿主侧能力；dsh bundle（Node 内，开放生态）管运行时内能力。profile 在每次拉起 dsh 时按工作区归属组装，组织工作区只载入策略白名单内的 bundle。

Alcove 核心不含任何 Cloister 代码，也不含日常 bot。

## 与系列其它项目的关系

只通过插件和线协议关联，不共享代码库。总览与架构文档见 [Charter](https://github.com/yiyiSmallti/precinct-charter)。

## 文档

- [首期 PRD](docs/PRD.md)
- [首期执行 Spec](docs/SPEC.md)

## 状态

首期开发中。平台为 Windows 10+ x64 与 macOS 13+（[ADR 0002](https://github.com/yiyiSmallti/precinct-charter/blob/main/docs/adr/0002-macos-joins-the-mvp.md)）。

已有：对话与运行时、插件宿主与边车、市场与预装（`dovetail`、`daily`、`cloister`）、组织工作区、诊断包。验收是 `e2e/` 下的 Go 测试（`go test -tags e2e ./e2e/`，要 `ALCOVE_E2E_NODE` 与 `ALCOVE_E2E_DSH`），报告写到 `e2e/report/`，这个目录不入库。CI 在 Linux 上跑同一条命令，但日常 bot 几套要 `DOVETAIL_DAILY_TGZ`（打好的 `@precinct/daily`）、市场一套要 precinct-dovetail 的检出，CI 里没有，所以这几套在 CI 里是跳过的，只在本机跑过。0.1.0 的 macOS 与 Windows 安装包在基线机上验过，见 [安装包验证](docs/validation/2026-11-02-packages.md)。

个人版整链验收是 `e2e/personal.sh`（Charter 架构文档第 7 节第 8 条的个人那条）：同一个核心、同一份数据目录里，配置 API → 与日常 bot 对话并设提醒 → 打开个人工作区完成一个要审批的任务 → 两个运行时空闲退出 → 提醒到点由边车弹出。在 Linux 上它把被测进程放进单独的网络命名空间并抓下发出的全部流量，由测试判定是否只有模型端点；别的系统上只跑链路，报告里那一步记为 SKIP。它要 precinct-dovetail 的检出来打 `daily` 包，CI 里是单独的 `personal` 工作流。它不从安装包装起（那是 A5），模型是假的。

组织版整链验收的第一段是 `e2e/org.sh`（同一条的组织那条）：真实的 daily 与 cloister 两个插件、真实的 Cloister 和 Gatehouse 接在一个 Alcove 核心上，走完扫码加入 → 管理员批准设备 → 文件夹设为组织工作区 → 回合经网关出去，手机号和客户名在到外部模型的请求里是占位符、回到对话里是原值 → 组织的审计里只有类别与计数 → 另一台设备把工作区接走后本机只读（模型写不进文件，要求提权也不问用户、直接不给），写入权回来后又能写 → 退出组织后文件夹从本机删掉、旧的网关令牌失效。模型提供方、企业微信、云沙盒编排是假的。它要 precinct-dovetail、precinct-cloister、precinct-gatehouse 三个检出和一个 Postgres。后面几段还没做：在企微里与 bot 对话和提醒、派云端任务、主力环境切换。

还没做：

- Developer ID 公证与 Windows 代码签名（没有证书；`installer/build.sh` 的 `ALCOVE_SIGN` 可以用自己的证书签名）。
- 自动更新：代码已在，首期安装包不配更新源（PRD 0.3）。
- 安装包瘦身之后的 Windows 安装包还没有重新打和验。
- Windows 上的托盘、中文输入法、讲述人、远程桌面未测。

许可：MIT。
