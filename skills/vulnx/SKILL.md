---
name: vulnx
description: 使用用户提供的 Vulnx 查询在线 CVE 情报，核对影响条件并形成待验证候选，不直接扫描资产。
license: AGPL-3.0-only
compatibility: 需要执行环境中的 vulnx 和到情报服务的网络；技能文件不代表工具已安装。
---

# Vulnx 候选情报

这是可选工作流。工具缺失或服务不可用时说明限制，继续已有证据分析，不自动安装或升级，也不阻止 ARTEX 其他能力。

1. 定位当前环境中的 vulnx，运行 `vulnx version --disable-update-check` 和 `vulnx search --help`，记录版本、二进制来源/hash。帮助是当前安装版本的参数契约。
2. 先确认产品、厂商、组件和版本；下面占位词必须替换为实际产品关键词/CVE。查询会向第三方服务发送关键词，避免提交私有目标、内部版本清单或秘密；仅在任务授权的在线查询范围内使用。

```sh
vulnx search 'product-keyword' --limit 10 --json --disable-update-check --timeout 30s
vulnx id CVE-YYYY-NNNN --json --disable-update-check --timeout 30s
```

3. 保存每次查询的原始 JSON、stderr、退出状态、UTC 时间、服务来源和查询参数（脱敏），独立目录避免覆盖。核对返回的厂商/产品、受影响版本、修复版本、利用前提、参考链接和数据更新时间；优先核对厂商原始公告。数据未提供更新时间时明确未知，不用查询时间替代。
4. 输出候选表：CVE、产品匹配依据、尚未证实的条件、原始情报文件、公告链接、可能的本地模板及模板版本差异。返回模板/PoC信息不授权下载或执行。将候选交给 nuclei 工作流前，确认模板和目标已授权；缺模板时保留候选并报告无法验证。

无 API key 可能限流。429 最多一次有限退避重试；仍失败则保留错误并停止查询。认证由用户通过正常配置提供，不自动运行 auth，不将密钥写入命令、日志或共享产物。服务错误、空结果、产品名称或版本匹配均不证明有漏洞或安全。情报候选与实际验证发现分开报告。

官方命令及许可：[projectdiscovery/vulnx](https://github.com/projectdiscovery/vulnx)。
