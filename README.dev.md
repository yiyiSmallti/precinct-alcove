# 开发

- Go 1.27+（`go.mod` 锁定；MyGo 需要）。纯 Go，`CGO_ENABLED=0` 交叉编译 Windows。
- e2e 需要 Node 24、pnpm 与锁定版本的 dsh：

```sh
mkdir -p /tmp/dsh && cd /tmp/dsh && npm init -y && npm i @deepseek-ai/dsh@0.2.1-rc.5
ALCOVE_E2E_NODE=$(command -v node) \
ALCOVE_E2E_DSH=/tmp/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js \
go test -tags e2e ./e2e/ -count=1 -v
```

报告写到 `e2e/report/*.md`，CI 每次运行都作为构件上传。

`TestDaily`（Dovetail D2：daily 插件在真核心与 dsh 里跑通）要一个打好的 `@precinct/daily` 包，没设就跳过，CI 不跑它（约 2.5 分钟，含一个两分钟的提醒）。在 precinct-dovetail 里构建：

```sh
plugins/alcove/daily/build.sh && go run ./registry/cli pack -o /tmp/daily plugins/alcove/daily
DOVETAIL_DAILY_TGZ=$(ls /tmp/daily/precinct-daily-*.tgz) go test -tags e2e -run TestDaily ./e2e/ -count=1 -v   # 加上面两个变量
```

`DAILY_E2E_MODEL_LOG=<文件>` 把每次发给假模型的请求按行记下，排查脚本化回合时用。

# 打包

在 Mac 上（`.app`、签名与 `.dmg` 需要 macOS；Windows 安装包另需 NSIS：`brew install makensis`，Linux 上 `apt install nsis` 也能打 Windows 安装包）：

```sh
installer/build.sh                    # macOS arm64 与 amd64 各一个 .app/.dmg + Windows x64 安装包，产物在 build/
installer/build.sh darwin-arm64       # 只打一个平台
ALCOVE_RESTAGE=0 installer/build.sh   # 复用已备好的运行时
```

`installer/stage-runtime.sh` 把随包运行时（Node 24 官方构建，按 SHASUMS256 校验；锁版本的 dsh 与 pnpm；pnpm 垫片；bridge 包）放进 `resources/`，`mygo build` 再把它装进应用的资源目录（Windows 在 exe 旁，macOS 在 `Contents/Resources`）。npm 按目标平台装原生可选包，不跑安装脚本（它们会为构建机编译）。每个平台一份，放 `resources/<平台>/runtime`，约 300 MB，不进仓库。为了体积（通用版 `.app` 1170 MB、`.dmg` 256 MB，现在 arm64 为 308 MB、60 MB，amd64 为 315 MB、80 MB）：

- macOS 不打通用版，arm64 与 amd64 各一个包，各自只带本 CPU 的 node 与原生包。
- dsh 的子包把 zod 锁死在一个版本，npm `overrides` 让全树共用这一份，否则每个子包各装一份（约 310 MB）；升级 dsh 时跟着改 `ZOD_VERSION`。
- `prune` 删掉运行时不加载的东西：sourcemap、类型声明、README 与 CHANGELOG、别的平台的预编译文件，以及 sdk profile 之外的实验插件用的语音识别引擎（sherpa-onnx）与 inspector。
- 不带 LibreOffice Kit 的引擎（单个 CPU 约 150 MB）。dsh 的 Office skill 在任务第一次要转换 Office 文档时问用户，同意后用随包的 node 与 pnpm 把引擎装进任务目录（约 20 秒）。没有网络或 npm 源时这项不可用，内网要配 npm 镜像。

配置在 `mygo.json`：`alcove://` 链接注册（`urlSchemes`）、macOS 13+、随包 node 的 JIT entitlement（`installer/node.entitlements.plist`）。

还差用户提供的部分，到位后加进 `mygo.json`：

- macOS 签名公证：`macos.signingIdentity` 填 `Developer ID Application: … (QHNTJ2FBJY)`，`macos.notarize.keychainProfile` 填 `xcrun notarytool store-credentials` 存的配置名。没有时为临时签名，只能在构建它的 Mac 上运行。
- Windows 代码签名：`windows.certificate`（.pfx，口令在 `MYGO_WINDOWS_CERTIFICATE_PASSWORD`）。
- 自动更新：`go tool mygo keygen` 生成密钥对，公钥与发布位置写进 `updates`（`publicKey` 与 `url`），私钥只放发布机或 CI 密钥（`MYGO_UPDATER_PRIVATE_KEY`）。没有 `updates` 的构建不检查更新，设置页显示"不能自动更新"。

应用内更新（A5）：启动时与每 24 小时检查一次，设置页"更新"可手动检查、看更新说明、下载安装（有差量包时只下差量）并重启。"更新源"填企业镜像的 https 目录（放一份发布文件的拷贝）后，清单与文件都从镜像取，但仍须是同一把密钥签的包。
