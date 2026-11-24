# 容器基础镜像维护

`Dockerfile` 的 `python:3.12-slim-bookworm` 使用 Docker Hub 官方 `library/python` 顶层多架构索引 digest。该索引同时提供 `linux/amd64` 和 `linux/arm64`；构建器按目标平台选择子镜像。固定索引保障基础镜像身份，后续 apt、NodeSource 和 npm 下载仍遵循 Dockerfile 的现有安装方式。

根目录的 Dependabot `docker` 更新器每周检查 `python`，保留 Python 3.12 系列。维护者审查 digest 更新 PR 时，通过官方 Registry 或 `docker buildx imagetools inspect python:3.12-slim-bookworm` 取得顶层索引，确认两个 Linux 平台各有一个子 manifest，并核对 PR 的 digest。不要使用本机 image ID 或单个平台的 manifest digest 替代索引。

更新基础镜像后，按 [CI 契约](ci.md) 连续执行两次相同镜像构建，记录缓存与构建结果，随后完成既有容器启动和浏览器工具检查。Dependabot 创建 PR 仅表示发现可用更新，合并仍取决于这些实际验证。

PostgreSQL 的消费入口包括根目录 Compose、质量与发行工作流、离线工作流输入及其默认表达式。离线生成器要求操作者传入精确 PostgreSQL 16 minor tag 和索引 digest，并按目标架构核验、拉取与记录实际镜像。当前这些入口继续使用各自既有引用，更新时需要人工核对全部消费入口。

[GitHub 官方支持表](https://docs.github.com/en/code-security/reference/supply-chain-security/supported-ecosystems-and-repositories)分别列出 `docker` 与 `docker-compose` 更新器。Compose 更新器覆盖实际 Compose 清单；Python 生成模板、离线工作流输入和表达式没有由这两个更新器提供的跨文件同步契约。因此当前只启用实际覆盖 Dockerfile 的 Python 自动更新。PostgreSQL 的统一固定与刷新需要先明确这些入口的同步维护方式。

操作者通过 `ARTEX_IMAGE`、`ARTEX_TAG` 选择 ARTEX 镜像，离线包通过发行资产确认 ARTEX 镜像身份，继续遵循现有部署与发行流程。
