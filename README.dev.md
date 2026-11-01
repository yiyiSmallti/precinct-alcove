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
