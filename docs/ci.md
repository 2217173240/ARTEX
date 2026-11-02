# 持续集成

`quality` 是主分支规则要求的合并检查。代码变更必须通过全部任务；任何失败、取消、缺失或意外跳过都会阻止该检查成功。

## 触发与并发

- PR 更新会取消同一个 PR 的旧运行，只验证最新版本。
- 每次 main push 使用独立并发组，后续提交不会替换已排队的提交。
- 只修改根目录 `README.md` 或 `docs/` 下 Markdown 文件时，保留路径分类和必需的 `quality` 检查，跳过编译、测试和打包任务。
- 路径分类使用 PR 的三点差异或 push 的前后提交差异，检查重命名两端。缺少可靠差异时执行全量检查。
- 手动触发 `quality` 始终执行全量检查，可用于验证缓存和运行时间。
- CodeQL 保留 Actions、Go、JavaScript/TypeScript、Python 四种语言、默认查询、remote threat model 和每周扫描。文档路径过滤只影响 push/PR 触发，每周及手动扫描仍检查完整源码。

## 校验范围

| 任务 | 保留的校验 |
| --- | --- |
| backend-race | 动态枚举全部根模块 package，分成三个互不重叠的分片；每个 package 使用独立 PostgreSQL 数据库，执行 `go test -race -count=1`，仅排除需要真实模型凭据的既有 `TestLiveContextReview` |
| backend-tools | 两个已修补的嵌套 Go 模块、静态采集回归、CI 辅助程序回归 |
| frontend | Node 测试、TypeScript、翻译覆盖、静态导出、npm 依赖审计 |
| native | Windows、macOS、Linux 的 AMD64/ARM64 普通构建和内嵌界面构建、原生回归、安装与升级生命周期 |
| image | 内嵌二进制、容器启动、离线浏览器工具、镜像各层私有文件排除 |
| security | 采集依赖与运行回归、发行辅助程序、发布输入、当前文件和完整提交历史密钥扫描 |

前端只导出一次，native 和 image 复用同一份 CI 产物。普通构建在对应原生平台完成，避免 Ubuntu 后端任务顺序编译五个交叉目标。发布流程继续独立构建生产前端，校验完整发行资产后发布实际测试过的镜像。

## 缓存与工具

Node 使用 `setup-node` 的 npm 缓存，仍通过 lockfile 执行 `npm ci`。Go 使用 `setup-go` 的模块与编译缓存；依赖键覆盖本次任务实际使用的模块。原生测试与发行构建统一 `-trimpath`，复用编译缓存，同时保留 `-count=1` 强制执行测试。

PR 缓存限定在 merge ref，不能污染 main 缓存。main push 可更新可信缓存；release 和 offline workflow 的 `cache-mode: read` 继续限制缓存写入。缓存不保存应用配置、模型凭据、数据库、发行包或容器镜像层。

Linux 打包在每个任务中下载一次固定版本的预编译 nFPM，并校验 SHA-256。DEB、RPM 和 previous 包复用该工具，分别保留安装生命周期验证。仓库没有 Rust 构建，无需 Rust 缓存。

Windows 打包在每个任务中安装一次固定版本、经过 SHA-256 校验的 WiX。当前版本、previous 包和跨架构包复用同一工具；公共打包入口未指定 `WIX_BIN` 时仍使用独立、经过校验的工具安装。

## 耗时判读

比较相同 workflow、事件、平台及成功状态的运行，分别记录排队、准备和执行耗时。准备包含 checkout、工具安装、依赖恢复、产物传输与收尾；执行包含实际构建和功能验证。

GitHub Jobs API 只提供步骤时间。一个 `go test` 步骤可能同时包含编译与测试，不能将全部时间视为纯测试时间。异常或缺失的时间戳保留为不可用值。端到端耗时与所有 runner 耗时之和分别比较，并标明冷、热缓存及重试情况。
