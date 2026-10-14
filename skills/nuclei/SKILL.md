---
name: nuclei
description: 对已授权目标使用用户提供的 Nuclei 和选定本地模板做低速定向验证，保留原始证据并解释覆盖限制。
license: AGPL-3.0-only
compatibility: 需要执行环境中的 nuclei 和已审核本地模板；技能文件不代表工具已安装。
---

# Nuclei 定向验证

这是可选工作流。缺少二进制、模板或权限时报告缺项，继续任务中其他可用操作；不自动安装、下载、升级或扫描。ARTEX doctor 仅检查 PATH/目录，不执行工具或证明版本兼容。

1. 使用当前执行环境的程序查找能力定位 nuclei；显式运行 `nuclei -version`、`nuclei -h` 检查版本及参数。检查用户提供的本地模板路径、模板发布版本或 git commit、文件 SHA256 与许可。工具和模板版本分别记录，不把在线情报更新时间等同于本地模板版本。
2. 从资产产品、版本及 CVE 情报确定候选，阅读具体模板内容，核对协议、请求、副作用、重定向和外连目的地。产品匹配仅是线索。只选择具体已审核的本地模板文件，不使用整个模板库、远程模板 URL 或仅按 severity 批扫。
3. 确认目标和模板操作都在用户授权范围内。核对本地配置没有引入额外目标、代理、报告上传、回调或模板源；使用已审核配置和当前版本帮助确认参数。默认禁用 OAST，排除 code/headless/fuzz 模板；需要这些能力时先确认授权和依赖。限速不替代对模板副作用的检查。
4. 每次调用使用独立任务目录。下面是替换占位符后的命令形状，不能直接执行占位目标；`-duc` 关闭更新检查，`-ni` 排除 OAST。同时在本次进程环境设置 `DISABLE_NUCLEI_TEMPLATES_PUBLIC_DOWNLOAD`、`DISABLE_NUCLEI_TEMPLATES_GITHUB_DOWNLOAD`、`DISABLE_NUCLEI_TEMPLATES_GITLAB_DOWNLOAD`、`DISABLE_NUCLEI_TEMPLATES_AWS_DOWNLOAD`、`DISABLE_NUCLEI_TEMPLATES_AZURE_DOWNLOAD` 为 `true`，避免缺模板触发下载。

```sh
nuclei -config './reviewed-config.yaml' -u 'https://authorized-target.example' \
  -t './reviewed-template.yaml' -duc -ni -rl 2 -c 1 -bs 1 \
  -timeout 10 -retries 0 -jsonl -o './unique-run/findings.jsonl' \
  -store-resp -store-resp-dir './unique-run/responses'
```

保留 stdout/stderr、退出状态、开始/结束时间及取消/超时状态。按当前版本保留 JSONL 中原始请求响应，不启用 omit-raw；响应目录可保留未命中请求证据。原始证据可能含目标凭据或个人信息，只保存在授权私有位置；对外副本脱敏，不打印凭据或带密钥的命令。取消后确认进程结束；未知执行结果先核对现有日志，不自动重跑。

报告记录授权范围引用、实际目标、完整参数（凭据脱敏）、二进制来源/版本/hash、模板来源/版本/ID/hash、原始结果及响应路径、执行状态、命中复核与覆盖限制。命中需结合请求响应确认；错误、跳过、鉴权不足、模板缺失分别列明。零命中只说明本次选定模板未观察到命中，不能证明目标安全。

官方参数：[Nuclei running](https://docs.projectdiscovery.io/opensource/nuclei/running)。按安装版本帮助核对，勿猜测兼容性。
