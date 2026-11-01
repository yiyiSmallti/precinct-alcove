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
