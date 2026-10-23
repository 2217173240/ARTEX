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

使用 `go.mod` 指定的 Go 版本、Node.js 22.12 或更新的 22.x、Python 3，以及 Docker（运行隔离 PostgreSQL 测试）。源码安装和运行方式见 [README](README.md#安装)。仅查看前端可在 `web` 目录运行 `NEXT_PUBLIC_MOCK=1 npm run dev`，使用内置合成数据，不需要模型密钥。

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
unset OPENAI_API_KEY ANTHROPIC_API_KEY
export ARTEX_LLM_PROVIDER='' ARTEX_LLM_BASE_URL='' ARTEX_LLM_MODEL='' ARTEX_LLM_PROXY=''
index=0
for package in $(go list ./...); do
  index=$((index + 1))
  database="artex_ci_${index}"
  docker exec artex-contrib-pg psql -U artex_ci -d postgres \
    -v ON_ERROR_STOP=1 -c "CREATE DATABASE ${database}"
  fixture_dsn="postgres://artex_ci:fixture_only@127.0.0.1:55432/${database}?sslmode=disable"
  ARTEX_PG_DSN="$fixture_dsn" ARTEX_INTERCEPT_TEST_DSN="$fixture_dsn" ARTEX_AUTH_TEST_DSN="$fixture_dsn" \
    go test -race -count=1 -skip '^TestLiveContextReview$' "$package"
done
go build -o /tmp/artex-contrib ./cmd/artex
python3 -m unittest discover -s skills/api-recon/scripts -p 'test_harvest_static.py'
docker stop artex-contrib-pg
```

检查每个包的结果；脚本遇到失败后停止，修复后用新的 fixture 重跑。若中途退出，可手动执行 `docker stop artex-contrib-pg` 清理本段命令创建的容器。`TestLiveContextReview` 是显式连接私有模型配置的测试，不属于常规贡献验证。

本地替换的嵌套依赖不会被根目录 `go test ./...` 收录。涉及依赖补丁时额外执行 `(cd third_party/go-mitmproxy && go test -race -count=1 ./internal/helper)` 和 `(cd third_party/norma && go test -race -count=1 ./...)`；源码来源、许可、所保留测试和未收录的上游测试分别见各模块补丁说明。Python 生命周期回归需要 Python 3，可用 `ARTEX_TEST_PYTHON` 指定解释器；原生 CI 明确检查解释器并执行该回归，不将缺解释器的跳过视为验证。

新增备注与日历 API 集成测试使用 `ARTEX_NOTES_TEST_ADMIN_DSN` 和 `ARTEX_CALENDAR_TEST_ADMIN_DSN` 指向本段隔离 PostgreSQL 的 `postgres` 数据库，测试自行创建并移除临时数据库；需要 fixture 角色具有创建数据库权限。CI 已配置这些变量。缺少变量时会明确跳过，不能视为已验证。不要指向用户数据库。

前端检查与 CI 相同：

```bash
cd web
node --test src/lib/*.test.mjs
npx --no-install tsc --noEmit
NEXT_EXPORT=1 NEXT_PUBLIC_MOCK=1 npm run build
npm audit --audit-level=high
```

涉及后端、前端或采集脚本时运行对应检查，并在 PR 中写明实际执行结果和跳过项。完整检查定义见 [quality.yml](.github/workflows/quality.yml)。其跨平台检查验证编译，镜像检查验证启动和文件层；不代表已在每种操作系统上执行所有运行测试。

维护者遇到 PR 事件未生成 quality 运行时，可执行 `gh workflow run quality.yml --repo 2217173240/ARTEX --ref <branch>` 手动检查该分支。触发后确认实际运行的 `headSha` 等于 PR 当前 HEAD，全部检查成功后再正常合并；新增提交需要重新验证，手动触发成功不等于检查通过。

若触发返回 HTTP 422 `Actions has been disabled for this repository`，先检查仓库 Actions 页的具体提示；权限 API 的 `enabled=true` 不代表工作流一定能执行。本 fork 曾因 GitHub Actions 使用规模而被暂停，需维护者按页面提示重新启用。恢复后确认新运行实际生成且开始作业；停用期间没有作业的旧排队记录不能作为 CI 证据。若页面没有恢复入口或重新启用后仍被拒绝，参照 [GitHub 官方说明](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository) 联系 Support，保留现有合并检查。

修改采集脚本依赖时，在 `skills/api-recon/scripts` 中执行 `npm ci --ignore-scripts` 和 `npm audit --audit-level=high`。运行时需要系统 Chromium；Puppeteer Core 不下载浏览器。CI 对该依赖树执行同样的安装和审计。

本 fork 使用 `ghcr.io/2217173240/artex` 和本仓库的 GitHub Releases，正式版本及资产以本仓库 Releases 为准。发布流程将正式标签固定到实际 commit，确认它已合入默认分支，并要求该 commit 的 `quality` 成功；所有构建使用这一 commit，发布任务仅能读取缓存。维护者可在同一个已存在标签上手动重试未完成的发布，例如 `gh workflow run release.yml --ref v0.3.18 -f tag=v0.3.18`；工作流触发 commit 必须等于预检验证的标签 commit，已公开 Release 不会被覆盖。

流水线先在原生 runner 和隔离容器中验证安装包的安装、升级、移除和启动，再创建包含六个便携 ZIP、两个 Linux TAR.GZ、八个原生安装包（共 16 个资产）及 `SHA256SUMS` 的草稿。随后分别在原生 Linux AMD64 / ARM64 runner 上构建并测试镜像启动、版本和内嵌页面，将实际测试的镜像 digest 汇总为双架构清单。再次检查草稿资产、完整校验和及清单后写入版本与 `latest` 标签、附加 `IMAGE_DIGEST`，最后公开 Release。GHCR 使用仓库 `GITHUB_TOKEN`，无需 Docker Hub 凭据；首次创建的 GHCR 包默认私有，维护者还需在包设置中改为公开，并验证两个平台的匿名拉取。工具链升级应更新 Dockerfile 的版本，并通过镜像检查。

安装或发布脚本修改可运行 `python3 -m unittest discover -s scripts -p '*test*.py'`，并检查相关 shell 脚本语法。ZIP 仅含运行文件，源码安装和更新脚本在源码仓库中使用。

## 提交问题和 PR

普通问题请说明版本或 commit、触发步骤、预期与实际结果，并附脱敏日志。安全问题使用 [安全报告说明](SECURITY.md)。修复尽量围绕一个问题，保留现有用户数据和无关配置；代码变更应附能捕获原问题的适当回归验证。

MCP 权限回归需要隔离 PostgreSQL 和合成本地 MCP。覆盖所有传输方式的 Agent 提案、明确批准、版本冲突、配置变更、停用/删除后的旧客户端调用、重新批准后旧客户端仍失效，以及手动/启动发现和 ScopeSentry 的一致门禁。普通保存不能批准或触发连接，客户端输入不能设置服务端批准状态。升级不得按历史 enabled 自动授予批准；用已有配置验证内容和可见性保留、一次性重新授权，并检查凭据编辑的留空保留/显式清空。

CodeQL 告警重新扫描后，应核对准确 commit 及实际 source/sink；有测试证明的授权边界和扫描器结果分别记录。管理员明确批准内网端点是受支持能力，不能为移除告警禁止所有内网目标，也不能用声明或不再触发静态告警来代替运行时权限测试。应用 MCP 授权不隔离拥有宿主机或数据库权限的执行进程，部署与残余风险见 SECURITY.md。

PR 描述写清问题、最终行为、测试及限制。引用历史 issue、PR 或外部代码时，在实际 PR 描述中注明来源链接、作者、commit 和采用方式：直接 cherry-pick、改写或仅借鉴思路。直接引入提交保留作者，使用 `git cherry-pick -x` 记录来源。不要把未验证的候选分支说成原始 PR 快照。

本仓库以 `e6ec56999175509551a1e43a3ee3b053d0941bd9` 为原始基线；原上游为 `Autumn-27/ARTEX`。已采用的改进保存在本仓库的提交、测试与 [fork 集成说明](docs/fork-integrations.md) 中，开发和运行不依赖本地恢复档案；历史 issue、PR 的需求不代表当前版本已全部实现。PR195 的 DoTTak 原提交 `78d0cd0503e3e8229ebb7799f52be758f0ef0579` 通过带 `-x` 的 cherry-pick 引入为 `053d776`；模型超时思路借鉴 [r0th-m 的候选提交](https://github.com/r0th-m/artex-ymh-fork/commit/9215eb0ef481391381cf2b1424d639f5feca5379) 后重新实现，该候选不是已认证的 PR127 快照。候选中的独立会话、隧道及内网编排子系统未整体采用。

日常维护优先 squash 合并；需要保留恢复出的历史 DAG 时可使用 merge commit。合入前确认 `quality` 检查对应待合入的准确 HEAD；新提交需要新的检查结果。单维护者仓库的零批准设置不代表已获得独立 GitHub 审阅。

中文 / English 界面使用声明式 React locale，默认 `zh-CN`；只翻译应用拥有的界面文本，保留用户内容与原始证据。翻译字典沿用 hongvincent 的 PR193（`1368a9422de0e083f31e543b31e231730f1ba675`）并补充运行时键；约定及审计命令见 [i18n README](web/src/lib/i18n/README.md)。漏洞归并引入 RuoJi6（ji ruo）的源提交 `24d4726`，以带来源记录的 cherry-pick 落为 `121716a`，后续适配保留原报告和不可变证据。

本轮还以带 `-x` 的 cherry-pick 引入 ji ruo 的 `7db7b71`（本 fork 为 `94c67dd`）；分组界面适配借鉴 `d36812b`、`ada09f6`、`012030b`、`aca6e9a`，额外访问认证借鉴 `04c8c54`、`ead58f0`，发布流程借鉴 Ruo 的实现并使用本 fork 的仓库和镜像命名空间。历史整理保留原报告只读，逐条记录根因判断，仅在当前统一报告和检查完整后完成；常规 Reporter 仍保留正常报告工具。

## 原生发布包

原生包与便携包使用不同的编译渠道，安装版不得直接替换系统安装文件。`.github/workflows/native-packages.yml` 在六种原生 runner 中验证包元数据、安装/升级/卸载与启动；所有个人目录使用隔离 fixture。打包只收集已跟踪的 skills 和明确的静态资源，不能包含本地配置、数据或生成依赖。维护发布流程时保留准确提交 quality、主分支 ancestry、完整包集合与镜像摘要验证。使用现有发布工作流创建新版本，勿修改已公开版本的包。

本轮同源功能改写及未采纳差异见 [fork 集成说明](docs/fork-integrations.md)，注明实际来源 commit，不将 fork 提交等同于未认证的原始 PR。
