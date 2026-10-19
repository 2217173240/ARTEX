# ARTEX 离线部署

联网打包机从已公开的稳定 Release 取得 `IMAGE_DIGEST`，解析目标架构的镜像 digest，核对应用镜像的版本、源提交、仓库、许可证标签，然后保存镜像。目标服务器通过校验清单载入镜像并启动应用与 PostgreSQL。打包过程不编译当前工作区，不收集本机配置、用户 skills、凭据或数据。应用内置 skills 随发布镜像提供。

## 联网打包

打包机需要 Python 3.11+、Docker Engine、`docker manifest`，以及足够保存应用镜像、数据库镜像和压缩包的磁盘空间。目标为 Linux amd64 或 arm64；交叉架构打包只拉取和保存镜像。

```bash
make offline-package VERSION=v0.3.20 TARGETARCH=amd64 \
  POSTGRES_IMAGE=postgres:16.10-alpine@sha256:029660641a0cfc575b14f336ba448fb8a75fd595d42e1fa316b9fb4378742297
```

使用 `TARGETARCH=arm64` 生成另一架构。PostgreSQL 输入必须包含明确的 16.x 版本及完整 index digest；请按部署方的安全维护政策选择版本。上例固定版本用于可复现验收。`OUTPUT` 可指定新输出目录；已有同名交付包会使构建失败并保留原文件。构建失败会清理本次暂存目录，已拉取的镜像留在打包机供再次使用。

`.github/workflows/offline-package.yml` 支持手动指定稳定版本、架构及 PostgreSQL 固定引用，生成 Actions artifacts。它只读取已有公开 Release。尚未发布的当前分支和缺少匹配镜像标签的发布版本会被拒绝。artifact 下载完成后再交付至目标服务器，workflow 不向服务器部署。

交付文件为 `artex-v版本-linux-架构-offline.tar.gz` 及 `.sha256`。包内包含应用与 PostgreSQL 镜像、固定本地标签的 Compose、`.env.example`、`manifest.json`、`IMAGES`、`SHA256SUMS`、管理命令、许可证与说明。SHA256 能发现传输损坏；接收者应从可信通道取得外部 `.sha256`。分发方须同时提供对应发布提交的完整对应源代码及第三方许可证，源位置记录于 `manifest.json`；有离线源代码交付要求时应单独提供该版本完整源代码包。

## 目标服务器安装

目标服务器须已有运行中的 Linux Docker Engine、Docker Compose v2（支持 `up --wait` 和 `pull_policy`）、Bash、tar、od、tr、sha256sum 或 shasum。无需 Python、Go、Node 或镜像仓库连接。使用有 Docker 权限的专用操作用户。Docker daemon 架构必须与离线包一致。

```bash
sha256sum -c artex-v0.3.20-linux-amd64-offline.tar.gz.sha256
tar -xzf artex-v0.3.20-linux-amd64-offline.tar.gz
cd artex-v0.3.20-linux-amd64-offline
./artex-offline verify
./artex-offline install
./artex-offline status
```

`install` 校验全部交付文件，再检查引擎架构、载入并核对镜像 ID，创建随机 256 位数据库密码和独立 Compose 项目标识，等待两项服务健康。数据库不发布宿主机端口。Web 与代理分别绑定本机 `127.0.0.1:8787`、`127.0.0.1:8788`。安装通过 `pull_policy: never` 和 `--pull never` 使用本地镜像，容器使用独立 bridge 网络，可访问用户授权的目标与模型服务；运行能力取决于目标服务器的网络及模型配置。需要远程访问时，由管理员明确配置反向代理后自行验收。

安装初始化沿用发布版本的身份验证流程。首次所有权凭据在 `data/` 内，按应用安装说明使用。不要把数据库密码或首次设置凭据贴入公共日志。需要本地模型时，应在目标网络内另行准备模型服务与配置。

## 管理与恢复

```bash
./artex-offline stop
./artex-offline start
./artex-offline restart
./artex-offline logs
```

重复 `install` 保留 `.env`、`.project`、`data/` 和 PostgreSQL 命名卷。普通停止不会删除容器或数据。管理命令没有卸载、清理卷、自动更新、密码重置或数据库回退操作。升级前由管理员备份 `.env`、`.project`、`data/` 并使用 `pg_dump` 备份数据库；新版本迁移与回退应按应用兼容性单独计划。

安装失败后修复已报告原因，再执行 `install`。例如端口被占用时，修改本地 `.env` 中的 `ARTEX_PORT` 与 `ARTEX_PROXY_PORT` 为两个空闲端口再重试；其值只影响本机绑定。已经初始化数据库时保留原有 `POSTGRES_PASSWORD`。如果进程被强制终止遗留 `.install-lock`，确认没有安装程序运行后删除空锁目录再重试。

文件权限为 `.env` / `.project` 0600、`data/` 0700。管理命令拒绝这些状态路径的符号链接。若状态文件丢失，应从原备份恢复；工具会拒绝使用新密码接管已有数据。初始化在磁盘写入中断后可能只留下 `.env` 或 `.project`：确认尚无数据库卷或应用数据后，人工补齐丢失的项再重试（`.project` 格式为 `artex-offline-` 加 24 位小写十六进制）；已有数据库应恢复原项，避免连接到错误项目。

## 真实离线验收

```bash
python3 scripts/test_offline_lifecycle.py dist/offline/artex-v0.3.20-linux-amd64-offline.tar.gz
```

验收需要允许特权容器的独立 Docker 测试机。它先联网准备官方 Docker-in-Docker 验收镜像，再启动 `--network none` 的全新 Docker engine，使用空镜像仓库执行损坏校验拒绝、首次安装、应用健康与版本、停止、移除容器保留卷、再次载入安装、SQL / 文件 / 配置保留及权限检查。验收结束删除本次容器、卷和验收镜像，保留原离线包。测试不使用模拟依赖。

本功能参考 [RuoJi6/ARTEX PR #17](https://github.com/RuoJi6/ARTEX/pull/17) 的完整离线交付思路；核查 head 为 `c594efd2b14bfda4e2c067896879e57e618a8830`，作者为 `adeljck`，许可证为 AGPL-3.0。该源 PR 为 closed、未 merged；本功能独立实现与验收，源 PR 中的验收描述不作为本功能的运行证据。
