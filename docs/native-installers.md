# 原生安装与更新

下载与你的系统和芯片相符的安装包，安装后启动 ARTEX，连接已有 PostgreSQL；随后在网页设置管理员账号和模型。模型配置和实际任务由用户决定，安装与启动不会替你发起目标测试。

## 安装与打开

- Windows：运行 MSI，在开始菜单打开 **ARTEX**。**ARTEX Control** 打开本地控制页，**Stop ARTEX** 停止程序。安装位置是 `%LOCALAPPDATA%\Programs\ARTEX`。
- macOS 13+：运行 PKG，在「应用程序」打开 ARTEX。控制页可通过 `open -a ARTEX --args launch control` 打开；也可使用下面的命令停止。
- Debian/Ubuntu：`sudo apt install ./artex-<版本>-linux-<架构>.deb`。
- Fedora/RHEL 系：`sudo dnf install ./artex-<版本>-linux-<架构>.rpm`。

Linux 安装后在应用菜单打开 ARTEX，或运行 `artex launch`；菜单另有控制和停止动作。`amd64` 表示 Intel/AMD x64，`arm64` 表示 64 位 ARM。各系统按架构分别打包。

当前源码采用 Go 1.27.3，macOS 安装器与二进制统一要求 macOS 13+。历史安装包保留各自发布时的系统要求。升级安装包不会更改已有个人目录和数据库配置。

MSI 和 PKG 当前没有发布者签名；macOS 包未公证。正式证书需由发布者提供，云端构建不能代替签名身份。所有下载文件均可按 Release 的 `SHA256SUMS` 核对。

## 首次连接与停止

首次页面填写数据库连接信息。回环地址默认关闭数据库 TLS，远程地址默认验证 TLS 证书和主机名；密码不会回显到页面或日志。连接检查只检查连通性，启动主程序后才初始化数据表。安装包不自带 PostgreSQL，可连接自己已有的本地或远程数据库。

再次启动会打开正在运行的实例。关闭浏览器标签页会保留 ARTEX 运行；在控制页点击「停止 ARTEX」，或使用：

```sh
# Linux
artex launch stop

# macOS
/Applications/ARTEX.app/Contents/MacOS/artex launch stop
```

Windows 也可在 PowerShell 执行：

```powershell
& "$env:LOCALAPPDATA\Programs\ARTEX\artex.exe" launch stop
```

命令 `launch status` 返回本机状态；`launch control` 打开控制页。启动失败时，控制页给出日志位置。启动器退出或崩溃时，其后端会停止，不依赖遗留 PID 去终止其他进程。

已有配置无法读取或 JSON 内容无效时，控制页显示文件路径和错误类别，保留原文件。修复文件后停止并重新启动启动器；也可使用 `artex doctor -json` 查看配置诊断。只有缺少数据库连接配置时才进入首次设置页面。`ARTEX_PG_DSN` 可直接提供数据库连接，技能目录另由 `ARTEX_SKILL_DIR` 指定。

## 个人数据与升级

| 系统 | 默认个人目录 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\ARTEX` |
| macOS | `~/Library/Application Support/ARTEX` |
| Linux | `$XDG_DATA_HOME/artex`，未设置时为 `~/.local/share/artex` |

目录保存 `config.json`、运行数据、日志和可编辑 skills。启动器记录已安装 skill 文件的内容哈希及权限：升级时，只更新或移除仍与上次随附版本一致的文件；用户编辑、新增的文件和符号链接保持不变，主动删除的文件也不会恢复。新随附文件仅在目标不存在时安装，未记录在清单中的旧文件也会保留。`ARTEX_HOME` 可指定独立目录，`ARTEX_CONFIG` 可明确指定已有配置。安装包仅维护程序和随附资源，卸载不删除个人目录或 PostgreSQL 数据。

安装版的「检查更新」提供本仓库的安装包入口。先停止 ARTEX，再运行同系统/架构的新安装器，或用 apt/dnf 安装新包。安装版不直接替换系统安装文件；便携 ZIP/TAR.GZ 的更新仍由现有守护启动脚本和二进制更新器处理。

认证状态默认继续保存在既有个人目录或便携程序目录。`ARTEX_AUTH_DIR` 可指定独立目录，同时用于 `jwt.key` 与首次设置交付文件；该目录应由操作用户控制，并位于可浏览的数据工作区之外。目标缺失时可从显式旧程序目录、旧数据目录迁移密钥，已有目标及不同的旧凭据保留。仅替换原生程序不会主动改变默认认证目录。

外部工具并未全部打入原生包，可按使用需求安装并运行 `doctor` 检查。安装器所用的 Python、Go、.NET 和打包工具属于构建依赖，不是安装包运行的依赖。需要完整工具环境时使用仓库提供的 Docker 镜像。
