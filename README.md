# Alcove

Precinct 系列 · 基于 dsh（DeepSeek Harness）的轻量桌面客户端 + 插件宿主。

## 定位

一个人自己的角落：常驻的 Go 进程（MyGo 原生 UI + 客户端核心）、按需拉起的完整 dsh 运行时、按需拉起的插件子进程。安装包为完整构建，运行时随包签名发布，与客户端同版本号。

**裸装只有 dsh 基础功能**：个人身份与 API 凭据（钥匙串，注入 dsh 进程）、工作区、对话与本机任务执行（dsh 完整能力：工具、审批、提问表单、子代理、skills、MCP）、运行时生命周期、每工作区 profile 的 dsh bundle 管理（dsh 原生机制，用户可装任意社区 bundle）、只有"从文件安装插件包"一个入口的插件宿主。没有日常 bot、记忆、日程、组织功能，也没有市场。不依赖任何服务端，模型调用直连 OpenAI 兼容端点；需要路由或脱敏时可指向单独运行的本机 [Gatehouse](https://github.com/yiyiSmallti/precinct-gatehouse)。

其余一切都是 Alcove 插件，由 [Dovetail](https://github.com/yiyiSmallti/precinct-dovetail) 提供（只上第一方插件）：

- `dovetail`：插件市场本身。随安装包预装、可卸载；没有它宿主仍能从文件安装。

- `daily`：本机日常 bot（对话视图、记忆、日程提醒、信箱）。装上它就是个人版。
- `cloister`：组织功能（加入组织、工作区归属、同步与租约、主力环境切换、云端任务）。装上它就是组织版。

## 插件机制

- 插件包 = manifest + 可执行文件（任意语言，推荐 Go）+ 可选 dsh bundle + 签名。
- 子进程，JSON-RPC over stdio；宿主握手校验 manifest 与协议版本，按 manifest 声明的能力授权（设置页、工作区钩子、运行时启动钩子、provider 端点、后台、信箱、深链）。
- UI 为声明式贡献（设置页、侧栏项、卡片模板），由 Alcove 用 MyGo 渲染，插件不画像素。
- 插件携带的 dsh bundle 由 Alcove 装进运行时 profile，`--dump-config` 校验后在下次拉起 dsh 时生效。
- 参考模式：hashicorp/go-plugin（子进程 + RPC、握手、版本协商），改用 JSON-RPC over stdio 与 dsh 运行时一致。

两层插件：Alcove 插件（子进程，第一方）管宿主侧能力；dsh bundle（Node 内，开放生态）管运行时内能力。profile 在每次拉起 dsh 时按工作区归属组装，组织工作区只载入策略白名单内的 bundle。

Alcove 核心不含任何 Cloister 代码，也不含日常 bot。

## 与系列其它项目的关系

只通过插件和线协议关联，不共享代码库。总览与架构文档见 [Charter](https://github.com/yiyiSmallti/precinct-charter)。

## 文档

- [首期 PRD](docs/PRD.md)
- [首期执行 Spec](docs/SPEC.md)

## 状态

设计阶段。MVP 只做 Windows，macOS 为不承诺的开发构建。第一步是为期一周的技术验证：MyGo 原生模式在低配基线机上的表现、中文输入法、VDI 下的 GPU 绘制、dsh 运行时的冷启动与内存、完整构建安装包的体积、插件子进程的常驻开销。

许可：MIT。
