# 参与贡献

贡献者提供可复现的问题或代码变更，维护者结合测试与代码审阅决定是否合入；请先阅读 [LICENSE](LICENSE) 和 [README 的使用限制](README.md#许可与免责声明)。开发与验证使用本地隔离环境、合成数据和 mock，不连接真实测试目标或用户数据库。

## 从源码开始

Fork 本仓库后克隆自己的 fork，创建一个用途明确的分支：

```bash
git clone https://github.com/<your-account>/ARTEX.git
cd ARTEX
git switch -c fix/short-description
go mod download
cd web
npm ci --ignore-scripts
cd ..
```

使用 `go.mod` 指定的 Go 版本、Node.js 22、Python 3，以及 Docker（运行隔离 PostgreSQL 测试）。源码安装和运行方式见 [README](README.md#安装)。仅查看前端可在 `web` 目录运行 `NEXT_PUBLIC_MOCK=1 npm run dev`，使用内置合成数据，不需要模型密钥。

## 本地验证

后端测试会建表、迁移和修改数据。以下命令在 Bash 中执行，每个包使用一个新的测试数据库；容器和凭据仅用于本地 fixture。不要替换为已有数据库的连接串。未配置 PostgreSQL 时，部分测试会跳过，不能视为数据库路径已通过。

```bash
set -euo pipefail
docker run --name artex-contrib-pg --rm -d \
  -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=artex_ci -e POSTGRES_PASSWORD=fixture_only \
  -e POSTGRES_DB=postgres postgres:16
until docker exec artex-contrib-pg pg_isready -U artex_ci -d postgres; do sleep 1; done

export ARTEX_CONFIG=/dev/null ARTEX_REVIEW_LIVE_CONFIG=''
export OPENAI_API_KEY='' ANTHROPIC_API_KEY=''
export ARTEX_LLM_PROVIDER='' ARTEX_LLM_BASE_URL='' ARTEX_LLM_MODEL='' ARTEX_LLM_PROXY=''
index=0
for package in $(go list ./...); do
  index=$((index + 1))
  database="artex_ci_${index}"
  docker exec artex-contrib-pg psql -U artex_ci -d postgres \
    -v ON_ERROR_STOP=1 -c "CREATE DATABASE ${database}"
  fixture_dsn="postgres://artex_ci:fixture_only@127.0.0.1:55432/${database}?sslmode=disable"
  ARTEX_PG_DSN="$fixture_dsn" ARTEX_INTERCEPT_TEST_DSN="$fixture_dsn" \
    go test -race -count=1 -skip '^TestLiveContextReview$' "$package"
done
go build -o /tmp/artex-contrib ./cmd/artex
python3 -m unittest discover -s skills/api-recon/scripts -p 'test_harvest_static.py'
docker stop artex-contrib-pg
```

检查每个包的结果；脚本遇到失败后停止，修复后用新的 fixture 重跑。若中途退出，可手动执行 `docker stop artex-contrib-pg` 清理本段命令创建的容器。`TestLiveContextReview` 是显式连接私有模型配置的测试，不属于常规贡献验证。

前端检查与 CI 相同：

```bash
cd web
node --test src/lib/*.test.mjs
npx --no-install tsc --noEmit
NEXT_EXPORT=1 NEXT_PUBLIC_MOCK=1 npm run build
npm audit --audit-level=high
```

涉及后端、前端或采集脚本时运行对应检查，并在 PR 中写明实际执行结果和跳过项。完整检查定义见 [quality.yml](.github/workflows/quality.yml)。其跨平台检查验证编译，镜像检查验证启动和文件层；不代表已在每种操作系统上执行所有运行测试。

本 fork 的修复以当前源码为准，README 中沿用的上游镜像和发布包不一定包含这些改动。验证本 fork 请从源码构建。发布流程要求准确提交的 `quality` 已通过；Docker 发布还需维护者显式设置 `DOCKER_IMAGE` 目标和相应发布凭据。流水线没有配置目标时跳过镜像发布。工具链升级应更新 Dockerfile 的版本，并通过镜像检查。

## 提交问题和 PR

普通问题请说明版本或 commit、触发步骤、预期与实际结果，并附脱敏日志。安全问题使用 [安全报告说明](SECURITY.md)。修复尽量围绕一个问题，保留现有用户数据和无关配置；代码变更应附能捕获原问题的适当回归验证。

PR 描述写清问题、最终行为、测试及限制。引用历史 issue、PR 或外部代码时，在实际 PR 描述中注明来源链接、作者、commit 和采用方式：直接 cherry-pick、改写或仅借鉴思路。直接引入提交保留作者，使用 `git cherry-pick -x` 记录来源。不要把未验证的候选分支说成原始 PR 快照。

本恢复版本以 `e6ec56999175509551a1e43a3ee3b053d0941bd9` 为原始基线；原上游为 `Autumn-27/ARTEX`。本地恢复档案收录 69 个 issue、65 个 PR，并保留历史元数据，档案位于 Git 仓库之外，尚未完整恢复讨论和全部原始版本。PR195 的 DoTTak 原提交 `78d0cd0503e3e8229ebb7799f52be758f0ef0579` 通过带 `-x` 的 cherry-pick 引入为 `053d776`；模型超时思路借鉴 r0th-m 候选提交 `9215eb0ef481391381cf2b1424d639f5feca5379` 后重新实现，该候选不是已认证的 PR127 快照，也未整体合入。

日常维护优先 squash 合并；需要保留恢复出的历史 DAG 时可使用 merge commit。合入前确认 `quality` 检查对应待合入的准确 HEAD；新提交需要新的检查结果。单维护者仓库的零批准设置不代表已获得独立 GitHub 审阅。
