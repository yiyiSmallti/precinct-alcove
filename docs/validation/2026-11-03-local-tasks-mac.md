# 本机任务确认卡 Mac 验证（2026-11-03）

在 Mac mini（M4，macOS 27.0.1）上，用 main 构建的 Alcove 跑通了云端 bot 派任务的完整流程：派任务、设备弹确认卡、在组织工作区里运行、拒绝、运行中停止、运行中追问，结果都回到了 bot。a、b、c、d 四项都通过。第一次跑 a 时失败了，原因是打包产物里缺文件（见“发现的问题”第 1 条），bot 收到了 `failed`。换成完整的运行时以后重跑通过。

## 版本与环境

| 项 | 版本 |
|---|---|
| precinct-alcove | main f9369ce（alcove#22，bridge 0.1.1） |
| precinct-cloister | main 460ff60（cloister#10，本机任务 API 1.3.0），用 `fake/` 假实现，`FAKE_REQUIRE_APPROVAL=1` |
| precinct-dovetail | main 50e5a14（dovetail#16），`plugins/alcove/daily` 与 `plugins/alcove/cloister` 用各自的 `build.sh` 构建，`dovetail pack` 打包 |
| dsh | 0.2.1-rc.5；pi-ai 1.4.0 |
| Node | 24.11.1（已装 Alcove 0.1.0 自带的那份） |

测试版应用和用户已装的 Alcove 分开（`~/Applications/AlcoveDev.app`，bundle id `com.precinct.alcove.devtest`），数据目录用 `ALCOVE_DATA_DIR` 隔离到 `~/IdeaProjects/precinct-mac-retest/lt/data`。下面这些地方和正式安装包不同，原因见“发现的问题”：

- 用 `go build ./cmd/alcove` 构建，没走 `installer/build.sh`。构建时临时拷了一份 `cmd/alcove`，只多加一处：信任由 `ALCOVE_LT_TRUSTED_PUB` 传入的测试公钥。两个插件包用这把测试密钥签名（`dovetail keygen` / `dovetail sign`）。仓库代码没改。
- 版本按 bundle 的 `CFBundleShortVersionString` 设成 `0.1.0`。用 `0.1.0-dev` 时，插件的 `host >=0.1.0 <0.2.0` 不匹配，安装按钮是灰的。
- Node 和 pnpm 用已装 Alcove 自带的。`ALCOVE_DSH` 先用的也是已装 Alcove 自带的 dsh，a 失败后改成一份 npm 装的完整 dsh 0.2.1-rc.5（同版本）。bridge 是 main 的 0.1.1。
- 假 Cloister 在 :8788，前面加了一个转发代理（:8787，即 `PRECINCT_CLOISTER_URL`）。代理只做一件事：给 `POST /v1/access/gateway-token` 的响应补上 `gateway_url`，因为假实现不发这个字段，不补的话启动钩子不给路由。`gateway_url` 指向一个模型桩（:8790），它讲 OpenAI Chat Completions，回固定文字，可以设延迟；它在 Gatehouse 的位置上。
- 界面操作由本机的 Codex（`codex exec` 和它的电脑控制功能）只在 AlcoveDev 里完成。企业微信回调、设备审批、派任务、停止、追问、查结果都用 curl 调假 Cloister。

## 准备

| 步骤 | 结果 |
|---|---|
| 装 `@precinct/daily`、`@precinct/cloister` | 通过：安装卡显示“签名有效”，cloister 装进 `worker`；两个插件都是“运行中” |
| 添加工作区 org-ws | 通过 |
| 加入组织 | 通过：设置页里服务地址已经是 `http://127.0.0.1:8787`，点“扫码加入组织”后出现登录卡（截图 08）；用 `fake_user` 走完回调，设备进入 pending，`/_fake/devices/{id}/approve` 之后显示“已加入组织 org_fake……已批准，策略 v3，已连接” |
| 设为组织工作区（内部） | 通过：显示“组织工作区 · 内部 · 版本 1 · 本机可写”；`workspaces.json` 的 owner 变成 `@precinct/cloister`，推送了 revision 1（截图 09） |

## 测试结果

| 项 | 结果 | 证据 |
|---|---|---|
| a 派任务 → 卡片 → 运行 → 结果回 bot | **通过**（第二次） | 卡片上有“在「org-ws」里运行”和“拒绝”两个按钮，写着“请在 20:49 前确认”。点运行后状态变 accepted，updates 依次是 `started`（`sessionRef: task-lt_a2`）和 `report`（`final: true, state: ready`，正文是模型桩的回复），卡片显示“已完成，结果已交回云端 bot。”。模型桩收到一次 `POST /v1/chat/completions`，带 `Bearer gwt_…`（网关令牌）和 `X-Gatehouse-Source: alcove`，说明走的是组织网关路由 |
| a 第一次 | 失败（打包问题，见“发现的问题”第 1 条） | 卡片显示“没能在本机启动：…no adapter registered for provider "precinct-gateway"”。bot 收到 `started`，接着收到 `failed`，里面带同样的原因（截图 10）。worker 失败时 failed 事件能回到 bot，这一点验证到了 |
| b 拒绝 | **通过** | 点“拒绝”后，假 Cloister 里的状态是 `declined`，没有 updates；卡片显示“已拒绝，bot 会告诉你可以改派云端或稍后再说。”；工作区里没有出现 b.txt（截图 13） |
| c 运行中停止 | **通过** | 模型桩延迟 180 秒。运行中卡片显示“正在工作区「org-ws」里运行……”和“停止”按钮（截图 14）。调 `POST /_fake/local-tasks/lt_c1/stop` 后，状态变 `ended`，updates 是 `started` 和 `ended`（带上停止原因），之后没有再报 report；卡片显示“云端 bot 撤回了这个任务：用户在企业微信里说不用做了”（截图 16） |
| d 运行中追问 | **通过** | 运行中调 `POST /_fake/local-tasks/lt_d1/input`，返回 `delivered: true`。模型桩收到第二次请求，最后一条 user 消息是“（来自云端 bot 的追问）补充：总结时提一下文件名。”，说明追问进了同一个会话。最终报告是这第二次请求的回复（`final: true`），状态 `ended`（截图 15、16） |

截图在本目录 `2026-11-03-local-tasks-mac/` 下。全部截图（按步骤编号）和每次 Codex 调用的提示与输出在 Mac mini 的 `~/IdeaProjects/precinct-mac-retest/lt/shots/` 与 `lt/codex/` 下。

## 发现的问题

1. **严重：安装包里的组织工作区都起不来。** mygo v0.7.4 打包时，拷资源会跳过所有以点开头的文件（`cmd/mygo/resources.go` 的 `hiddenName`，第 144、643 行），所以 `.app` 里 `runtime/node_modules` 下一个点文件都没有。pi-ai 1.4.0 在加载时要导入 `dist/providers/data/.manifest.json`，`llm-pi-ai` 因此加载失败（dsh 的报错是 `ERR_MODULE_NOT_FOUND`）。组织工作区用的 `precinct-gateway` 路由正好挂在这个适配器上，所以 initialize 返回 “no adapter registered for provider "precinct-gateway"”。对照方法：同一个 profile、同一组环境变量，用安装包里的 dsh 会失败，用 npm 装的同版本 dsh 能正常 initialize。Windows 包也是 mygo 打的，应该有同样的问题。修法有两种：`stage-runtime.sh` 绕开 mygo 拷资源（比如把运行时打成归档，首次启动时解开），或者让 mygo 保留点文件，并加一项检查：安装包里的 `llm-pi-ai` 必须能加载。
2. **正式构建装不了插件。** `cmd/alcove/main.go` 既没传 `TrustedKeys`，也没开 `AllowUnsigned`，所以安装包对任何 .tgz 都报 signature 错误，包括已经签名的。SPEC 里说的内置一方公钥还没接上。
3. **窗口在后台时不重绘。** 异步结果（安装卡片出现、安装完成、确认卡状态变化）要等下一次点击才显示出来。用户在别的应用里时，确认卡可能看起来没弹出来。没在前台单独复测。
4. 小问题：重启 Alcove 后，重启前已结束的卡片（a 第一次失败的那张）不再显示，重启后出现的卡片照常显示。cloister 对话页里有一条旧提示“正在等待管理员批准”，批准后没有更新。插件日志有一条 debug：“ui contribution "cloister" was not registered by this plugin”。
5. 测试环境的问题：假 Cloister 的网关令牌响应里没有 `gateway_url`，这次靠转发代理补上。如果要让假实现本身跑通组织工作区，要在 `fake/` 里加一个可配置的网关地址。

## 未覆盖

- 只有一台设备，没测多台设备抢答（`settled`）和确认超时（`expire`）。
- 没接真模型，也没接真 Gatehouse，模型桩回的是固定文字。没测需要审批或提问的操作。
- c 只确认了 bot 那边收到 `ended`，而且之后没有再报。dsh 回合有没有真的被 `session.cancel` 取消，没有单独去查会话日志。
