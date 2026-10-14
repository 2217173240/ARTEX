# 可选安全工具工作流

`skills/nuclei` 与 `skills/vulnx` 是供现有 Skill 系统加载的可选说明。技能目录存在不代表二进制、模板、网络或目标权限可用。没有新增启动下载、扫描、自动 seeder 或数据库默认可见性规则。保留用户已有禁用/删除状态；正常升级依赖既有原生包的技能 hash manifest 规则，不在启动时重建删除目录。

原生安装：技能文件随包分发，Nuclei/Vulnx 二进制和 Nuclei 模板由用户在执行环境单独提供。便携/源码运行需检查实际 SkillDir。Docker：查看所用镜像的 Dockerfile、版本/digest 和工具清单；本说明不保证任何现有或历史镜像预装工具。不能用宿主机 PATH 证明容器内可用，也不能用技能文件证明镜像供给完整。

`artex doctor` 对这两个工具只查 PATH，对技能文件只查存在；仅在设置 `NUCLEI_TEMPLATES_DIR` 时检查模板目录可读性。它不执行程序、不检查版本、不下载模板、不扫描，也不输出配置路径或凭据。optional missing 是 WARN；未知模板路径是 SKIP，均不因安全工具缺失单独产生失败退出码。

使用时分别记录二进制版本/hash、模板发布版本或 commit/hash、情报来源/更新时间、授权范围、命令参数、退出状态、原始 JSON/响应证据与覆盖限制。情报查询只给候选；低速扫描仍需要模板副作用与目标授权检查。零发现不能证明安全。两个技能正文提供完整操作步骤。

工具供应/升级候选需逐个记录：官方仓库、固定 release/tag 和准确 commit、OS/架构资产 URL、官方校验值或可审计 SHA256、许可证及随资产分发的许可文件、模板独立版本/hash、当前命令帮助与无网络版本检查结果。核对 Nuclei/Vulnx 工具许可和 nuclei-templates 许可，不能用本仓库技能说明的 AGPL 代替外部工具许可。升级先在隔离合成 fixture 验证参数和证据格式，再经明确授权替换；不使用 `@latest`、自动更新或未经核对的远程模板。以上是供应验收规则，非已完成的工具资产锁定清单。

参考：[Nuclei 官方运行说明](https://docs.projectdiscovery.io/opensource/nuclei/running)、[Vulnx 官方仓库](https://github.com/projectdiscovery/vulnx)。本工作流借鉴 RuochenPan / RC-CHN 的 [c65a1cdd](https://github.com/RC-CHN/ARTEX/commit/c65a1cdd10ad3109cd60d3ca4ee9cf608c2584da)，按当前架构改写；未采用其 startup seeder、强制默认可见性或预装镜像声明。
