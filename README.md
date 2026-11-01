# Alcove

Precinct 系列 · 基于 dsh（DeepSeek Harness）的轻量个人 Agent 桌面客户端。

## 定位

一个人自己的角落：常驻的 Go 进程（MyGo 原生 UI + 客户端核心）加一个按需拉起的完整 dsh 运行时。安装包为完整构建，运行时随包签名发布，与客户端同版本号。

单独可用：不依赖任何服务端。个人用户用自己的 API，有一个本机日常 bot（对话、记忆、日程、提醒）和若干个人工作区（工作任务）。模型调用直连 OpenAI 兼容端点；需要路由或脱敏时，可把端点指向单独运行的本机 [Gatehouse](https://github.com/yiyiSmallti/gatehouse)。

## 扩展面

- dsh 插件与 profile：运行时内的一切能力都是 dsh 插件，Alcove 不修改 dsh 核心。
- connector 接口：加入组织、工作区归属、同步与租约、bot 主力环境切换等组织功能全部在 connector 之后。Alcove 核心不含任何 Cloister 代码；Cloister connector 由 [Dovetail](https://github.com/yiyiSmallti/dovetail) 提供。

## 与系列其它项目的关系

只通过插件和线协议关联，不共享代码库。总览与架构文档见 [Charter](https://github.com/yiyiSmallti/charter)。

## 状态

设计阶段。第一步是为期一周的技术验证：MyGo 原生模式在低配基线机上的表现、中文输入法、VDI 下的 GPU 绘制、dsh 运行时的冷启动与内存、完整构建安装包的体积。

许可：待定。
