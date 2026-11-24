# 持续集成

`quality` 是主分支规则要求的合并检查。代码变更必须通过全部任务；任何失败、取消、缺失或意外跳过都会阻止该检查成功。

## 触发与并发

- PR 更新会取消同一个 PR 的旧运行，只验证最新版本。
- 每次 main push 使用独立并发组，后续提交不会替换已排队的提交。
- 只修改根目录 `README.md`、`AGENTS.md`、`CONTRIBUTING.md` 或 `docs/` 下 Markdown 文件时，保留路径分类和必需的 `quality` 检查，跳过编译、测试和打包任务。`SECURITY.md`、工作流、配置及混合代码变更继续执行完整检查。
- 路径分类使用 PR 的三点差异或 push 的前后提交差异，检查重命名两端。缺少可靠差异时执行全量检查。
- 手动触发 `quality` 始终执行全量检查，可用于验证缓存和运行时间。
- CodeQL 保留 Actions、Go、JavaScript/TypeScript、Python 四种语言、默认查询、remote threat model 和每周扫描。文档路径过滤只影响 push/PR 触发，每周及手动扫描仍检查完整源码。

## 校验范围

三个 Go 模块统一要求 Go 1.27.4。质量、原生包和发行工作流通过根目录 `go.mod` 选择构建工具链，独立运行保留模块时也遵循相同最低版本。默认使用 JSON v2 引擎，现有 `encoding/json` API 保留兼容语义；正常构建不设置 `GOEXPERIMENT=nojsonv2`。工具链变更继续运行既有全部检查，包括 JSONB 原始输入、模型协议及六个平台的原生生命周期。macOS 安装基线为 13.0。

Go 1.27.4 于 2026-11-10 发布，包含 cgo、Go 命令、编译器、链接器和 `net/http` 的安全修复，详见 [Go 官方发布记录](https://go.dev/doc/devel/release#go1.27.0)。重新构建并交付的产物才包含该工具链补丁。

CodeQL 的 Go 任务也显式选择同一工具链；其他语言不安装 Go，该分析任务不另外保存 Go 缓存。

| 任务 | 保留的校验 |
| --- | --- |
| backend-race | 动态枚举全部根模块 package，分成三个互不重叠的分片；分片按 `scripts/package-timings.json` 的实测时长做 LPT（最长优先）均衡，时长表缺失或包无数据时按默认估值，表整体缺失时退回轮转。每个 package 使用独立 PostgreSQL 数据库，执行 `go test -race -count=1`，仅排除需要真实模型凭据的既有 `TestLiveContextReview` |
| backend-tools | 两个已修补的嵌套 Go 模块、静态采集回归、CI 辅助程序回归 |
| frontend | Node 测试、TypeScript、翻译覆盖、静态导出、npm 依赖审计 |
| native | Windows、macOS、Linux 的 AMD64/ARM64 普通构建和内嵌界面构建、原生回归、安装与升级生命周期 |
| image | 内嵌二进制、容器启动、离线浏览器工具、镜像各层私有文件排除 |
| security | 采集依赖与运行回归、发行辅助程序、发布输入、当前文件和完整提交历史密钥扫描 |

前端只导出一次，native 和 image 复用同一份 CI 产物。普通构建在对应原生平台完成，避免 Ubuntu 后端任务顺序编译五个交叉目标。发布流程继续独立构建生产前端，校验完整发行资产后发布实际测试过的镜像。

## 缓存与工具

Node 使用 `setup-node` 的 npm 缓存，仍通过 lockfile 执行 `npm ci`。Go 使用 `setup-go` 的模块与编译缓存；依赖键覆盖本次任务实际使用的模块。原生测试与发行构建统一 `-trimpath`，复用编译缓存，同时保留 `-count=1` 强制执行测试。

前端另缓存 `.next/cache` 和 TypeScript 编译元数据，按平台、Node 版本、构建模式、依赖、配置和源码区分。每次仍执行全部 Node 测试、独立 TypeScript 检查和静态导出，不缓存测试结果或导出产物。

PR 缓存限定在 merge ref，不能污染 main 缓存。main push 可更新可信缓存；release 和 offline workflow 的 `cache-mode: read` 继续限制缓存写入。缓存不保存应用配置、模型凭据、数据库、发行包或容器镜像层。

PR 关闭后，独立 `cache-cleanup.yml` 使用 `pull_request_target: closed` 回收该 PR 的 `refs/pull/<number>/merge` 缓存。该任务不检出代码、不安装工具链、不读写构建缓存，仅授予删除缓存所需的 `actions: write` 和核实 PR 当前状态所需的 `pull-requests: read`；先核实 PR 已关闭，再通过分页 REST 查询和精确 ref 筛选逐 ID 删除。main 与开放 PR 的缓存保留。[GitHub 缓存管理文档](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manage-caches)说明了关闭 PR 后清理缓存的用途和事件权限。

image 与 Linux amd64 native 使用 Ubuntu 24.04、相同的三个模块依赖键及 `-trimpath` 编译参数。image 只读取缓存，两个任务在 frontend 成功后并行；frontend 因文档变更跳过或执行失败时，image 同样跳过。backend-race 的缓存键仅跟随模块依赖变化，执行脚本仍使用 `-count=1` 强制运行测试。

Linux 打包在每个任务中下载一次固定版本的预编译 nFPM，并校验 SHA-256。DEB、RPM 和 previous 包复用该工具，分别保留安装生命周期验证。仓库没有 Rust 构建，无需 Rust 缓存。

缓存命中以对应 setup action 的恢复日志或命中输出为依据。nFPM 的安装脚本每次执行下载与校验，没有单独配置 Actions 工具缓存；阶段记录的 `0s` 受整秒计时精度影响，不能据此判定缓存命中。分阶段计时用于定位耗时，setup-go 的缓存恢复与工具链安装仍需查看各自日志。

Windows 打包在每个任务中安装一次固定版本、经过 SHA-256 校验的 WiX。当前版本、previous 包和跨架构包复用同一工具；公共打包入口未指定 `WIX_BIN` 时仍使用独立、经过校验的工具安装。

## 耗时判读

每轮维护先通过 `gh` 读取最近 50 次 workflow 运行，包含同一运行的历史重试 attempts 和各自 jobs。比较相同 workflow、事件、平台及成功状态，分别记录排队、准备和执行耗时；收尾单列。准备包含 checkout、工具安装、依赖恢复及产物传输；执行包含实际构建和功能验证。

GitHub Jobs API 没有准确的逐 job 入队时间。run 创建到 job 开始的间隔包含依赖等待，只能作为开始前等待的上界。一个 `go test` 步骤可能同时包含编译与测试，不能将全部时间视为纯测试时间。异常或缺失的时间戳保留为不可用值。端到端耗时与所有 runner 耗时之和分别比较，并标明冷、热缓存及重试情况；历史重试的资源占用单列。

缓存保持 10 GB 项目预算，优先使用 setup action 的能力；按任务实际读取的依赖配置键，定期检查大小和命中日志。格式化、lint 和依赖审计使用 Ubuntu；macOS 并发按所有 workflow 合计不超过五个任务安排。前端产物通过实际消费关系传给 native 和 image，不能删除这些 `needs`。

修改构建或缓存行为时，在隔离输出路径用相同命令连续构建两次，记录时间及产物哈希。检查 Docker 依赖层是否位于变化频繁的源码之前、构建输入是否真实存在。缓存命中只减少可复用的准备与编译工作，功能测试仍实际执行。云端效果以 push 后的真实运行和匹配样本为准，不能把步骤拆分或不同事件的耗时差异直接归为提速。

维护准则见 [AGENTS.md](../AGENTS.md)，并参考 [GitHub 并发控制](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)、[依赖缓存](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)和[必需检查说明](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging)。

Windows 原生任务分别显示 WiX 安装校验、MSI 打包和完整安装生命周期验证。脚本输出各阶段的 UTC 时间与耗时，`msiexec` 启动前记录操作、包和日志位置，结束后记录进程 ID、退出码与耗时；这些诊断使用 PowerShell 信息流，工具路径的成功流输出保持为单个路径。

Linux 原生任务同样按阶段拆分：nfpm 固定版本下载与校验、DEB/RPM 打包与结构校验、容器内安装生命周期（升级路径与全新安装路径分开计时）。每阶段输出 UTC 时间戳与耗时，同一命令集与断言保持不变，仅将工具和包准备从生命周期步骤中分离，便于失败归因与耗时统计。生命周期步骤仍包含容器镜像拉取及包管理器依赖安装，其整体耗时不能视为纯测试时间。

## 取消运行的鉴别

先核对对应 run、attempt、job 的 check-run annotations，再结合步骤和时间线区分执行超时、账号取消与 runner 获取失败。[GitHub 官方取消流程](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-cancellation)包含最多五分钟的取消处理期限；job 的开始至结束时间跨度不能直接当作测试耗时，也不能仅凭超过配置上限推断 runner 失联或取消发起者。

2026-11-16 核对 round48 中四条取消记录，各自来自不同的运行：

| 运行 / attempt | Job 与记录跨度 | GitHub annotations 确认的原因 |
| --- | --- | --- |
| [42323457823 / 1](https://github.com/2217173240/ARTEX/actions/runs/42323457823/attempts/1) | Windows x64 `128253468868`，2400 秒 | 超过 35 分钟最大执行时间；没有可用的步骤明细。 |
| [42790693059 / 1](https://github.com/2217173240/ARTEX/actions/runs/42790693059/attempts/1) | Windows x64 `129743500779`，2400 秒 | 超过 35 分钟最大执行时间；九步成功，旧 MSI 复合步骤没有完成记录。 |
| [42797212838 / 1](https://github.com/2217173240/ARTEX/actions/runs/42797212838/attempts/1) | macOS ARM64 `129765738662`，901 秒 | 多次尝试后仍未获取 hosted runner，并有该平台容量不足提示。 |
| [41561573607 / 1](https://github.com/2217173240/ARTEX/actions/runs/41561573607/attempts/1) | Windows x64 `125673527152`，1397 秒 | 运行由账号 `2217173240` 取消；记录不区分人工操作与使用该账号的自动化。 |

两次 40 分钟跨度与超时 annotations 可以同时成立；额外五分钟的具体经过仍需 runner 或调度诊断，不能据此确认挂起机制。`steps: []` 单独只表示没有可用的步骤明细，不能证明 job 从未执行；上表 macOS 的获取失败判断依据是明确的 annotation。约 89 秒后重跑及其成功结果只说明后续运行恢复，不能替代原运行的取消原因证据。

获取归档日志时应跟随 API 重定向并检查最终响应；API 返回重定向不代表底层日志仍然存在。本次四份旧日志的最终响应均为 `404 BlobNotFound`，因此无法进一步定位旧 MSI 复合步骤内部的具体子操作或 runner 状态。保留已获取的 annotations、attempt 时间线与步骤结果，后续通过 PR #58 的 MSI 分阶段时间戳及独立日志继续定位。

MSI 详细日志保存在 runner 临时目录的 `artex-msi-logs`，以 `<artifact-prefix>-windows-<架构>-msi-logs` 上传并保留一天。诊断上传在普通失败后仍会尝试执行；安装前失败时可以没有日志，运行器丢失或强制取消也可能阻止上传。先按阶段定位下载、打包或生命周期操作，再决定是否需要进一步诊断。现有等待方式、35 分钟任务上限以及安装、升级、降级拒绝、架构和数据保留检查均保持原有约束；增加诊断不等同于已确认超时根因或取得性能收益。

需要复测完整流水线时，使用 `gh workflow run quality.yml --ref main --repo 2217173240/ARTEX`。该操作不会发布发行包或部署应用。等待前一次运行完成后，在相同提交再次触发，可检查缓存是否命中；所有功能测试仍会重新执行。
