# 同源功能集成

2026-10-12 的同步结果修复以 `146e560d308aa26b8371c60441d7b2f4ea04c879` 为基线，借鉴 [sharenjun 的 `19d9bbc`](https://github.com/sharenjun/ARTEX-main/commit/19d9bbcebfb56a6a8dff467c5357ee0a23d1e7b2)（作者 sharenjun，AGPL-3.0）对部分同步和截断的明确报告，按当前同步 API 重新实现。保留每目标、每类型 5000 条保护上限，区分达到上限与确认还有记录；报告无效记录、派生 IP 和企业范围写入错误，避免前端把未完成结果显示为成功。计数表示成功资产写入次数，包含更新和重复源记录，不代表新建或唯一资产数。请求取消后停止后续操作，已完成写入保留；遵循 [Go context](https://pkg.go.dev/context) 的请求取消传播和 [MCP 工具规范](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#error-handling)的错误报告语义。

本次采用上述结果正确性部分。上游持久化同步历史、后台恢复和独立任务完成协议涉及新的状态模型，仍作为单独候选保留；没有整体合入其业务映射、任务状态机或 CI。jiwoochris PR #22 的认证、初始化、密码长度和连接池补强已存在于本 fork 的 `a951e4a` 及后续改进中，本轮确认无须重复引入。

2026-10-11 的安全与可靠性增量以 `9b531c1850e789d4175bb7490be4e9208e50b0a3` 为基线：

| 来源 | 采用方式 |
| --- | --- |
| [jiwoochris PR #20 · 6f8e1b2](https://github.com/jiwoochris/artex-ko/pull/20)，合并作者 Jiwoo Chris Jung / jiwoochris | 借鉴首次设密所有权和改密撤销会话；使用独立 HMAC 域与当前数据库密码哈希，每次请求验证，保留现有 503、数据库原子初始化、限流、CSRF 和共享管理员。没有复制来源早期忽略数据库错误及仅用进程锁的实现。 |
| [sharenjun · be2de18](https://github.com/sharenjun/ARTEX-main/commit/be2de18d5a5f759bbce7f3c4b08d484cf7d142c4)，作者 sharenjun | 重写工作区和聊天附件文件访问为持有的 `os.Root`，保留原 API；本轮完全没有文本/工具产出的正常完成改为模型错误，保留工具后静默完成和取消/预算语义。 |
| [sharenjun · 032f81b](https://github.com/sharenjun/ARTEX-main/commit/032f81b861e308491b772674e49e2b3a51143536)，作者 sharenjun | 使用现有 yaml.v3 的节点树更新技能元数据，保留未知值及正文；MCP 更新清除旧 `mcp` 别名，无法安全处理的合并继承或别名关系返回错误并保留文件。 |

上述来源均为 AGPL-3.0，保留原作者归属；本 fork 根据当前架构适配，没有整体覆盖来源文件。当前管理员会话升级需重新登录；首次设密操作见 README，运行与文件访问边界见 SECURITY.md。

2026-10-10 的功能集成以本 fork 的 `62b30f715c46676f5081162222d1dc62ed3dfa91` 为基线。采用下列具体提交的功能或思路，按当前 Go / Next.js、共享管理员认证和任务生命周期重新实现；不是整体合入各 fork，也不是对缺失原始 PR 的身份认证。

| 来源 | 有价值的差异 | 本 fork 的实现 |
| --- | --- | --- |
| [jiwoochris/artex-ko · 310fbb5](https://github.com/jiwoochris/artex-ko/commit/310fbb5789331ea8208c864dcd880dfed2d8dfdf) | SSE 断线时补取会话 | 当前会话串行补取所有缺失页，按序号去重；切换任务、会话或恢复连接后丢弃旧响应。继承会话保持只读。 |
| [RuoJi6/ARTEX · 3254c90](https://github.com/RuoJi6/ARTEX/commit/3254c90a03a06e0b121ce2ac92ccf2d7752d94db)、[98321d8](https://github.com/RuoJi6/ARTEX/commit/98321d826265b317a6da7d71ecb0fb6f7ed1271a)、[0af7132](https://github.com/RuoJi6/ARTEX/commit/0af7132ab74103ea087aacbcf028965041a63f1f) | 日历窗口、连续日期范围、暂停来源 | 对已有任务启用每周或一次计划；明确时区，窗口并集，持久化暂停来源，复用当前准入、FIFO、归档和删除屏障。 |
| [dtqdtq01/artex-custom · f801877](https://github.com/dtqdtq01/artex-custom/commit/f8018775096c6746e7d2069e397826111c9f42c1)、[943eaa2](https://github.com/dtqdtq01/artex-custom/commit/943eaa24534e18aee4759d9a2c3188fd1a6e8a86) | 人工备注、处置归属和即时刷新 | 服务端共享管理员归属；分页备注、事务写入、继承与归档限制；人工处置保存当时状态，自动变化保留历史信息。备注完整归档恢复。 |
| [Rycar1/ARTEX · f07acef](https://github.com/Rycar1/ARTEX/commit/f07acefdab5fa312cc16bf87f082d6e7b4751a9e) | 失败冷却并扩展扫描，减少队首阻塞 | 当前项目没有该旧 Reviewer 扫描器。现有任务出队后执行，失败终结并继续下一项；增加实际失败和取消回归测试，保留现有队列实现。 |

来源作者分别为 jiwoochris、ji ruo / RuoJi6、dtqdtq01 和 Rycar1；具体作者信息以链接提交为准。沿用仓库 AGPL-3.0 许可与现有来源记录。

## 时间窗口

每周计划使用 ISO 星期（周一为 1，周日为 7）；结束时间早于开始时间表示跨午夜。一次日期范围从首日开始时间连续运行到末日结束时间，采用 `[开始, 结束)`。日期与时间都在计划保存的 IANA 时区解释，默认 `Asia/Shanghai`，不会随服务器时区变化。

夏令时重复的本地时刻选择最早对应的实际时刻；不存在的本地时刻会使一次计划校验失败，重复计划跳过该次窗口。Go 对本地时间歧义的行为见 [time.Date](https://pkg.go.dev/time#Date)，实现使用显式边界计算及测试确定本项目规则。

多个启用计划绑定同一任务时，只要任一窗口开放即可运行。手动暂停优先；立即运行是显式恢复，会启用计划并保留覆盖到下一个窗口结束。一次计划已经过期时需修改日期。计划的“窗口开放”与任务的“运行中 / 排队中”分别展示，受现有模型配置和并发上限约束。

重启只按当前窗口协调任务，不补跑过去的窗口。关闭窗口先落库暂停与撤销排队，再停止执行。停用、删除或移除最后一个计划后，已有计划暂停转为手动暂停，避免意外启动；正在运行的任务保持当前状态。恢复的归档暂停也归为手动，日历绑定保留。

## 备注与人工处置

现有认证仅提供一个共享 ARTEX 管理员，备注不承诺逐人审计。正文为 1–8000 个 Unicode 字符；只允许来源上下文修改，继承上下文可查看，归档排队及归档过程中冻结写入。归档恢复保留备注、作者、时间、ID 与最后人工处置信息；内部备注不自动写入公开漏洞报告。

最后人工处置记录保存 actor、时间和当时的状态，状态与记录同事务提交。自动复测或 Agent 改变状态后保留这条历史记录；界面展示其历史状态，不将其视为对新状态的人工认可。重复提交相同状态不刷新记录。

## 实现参考与边界

[MDN SSE 文档](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events)说明了断线重连、事件 ID 与代理缓冲。补取仅在实时连接断开时工作；代理仍应禁用缓冲。页面切换会丢弃进行中的旧补取响应，已有 HTTP 请求可能完成。

备注、归档和任务控制沿用数据库事务与既有锁序；[PostgreSQL 显式锁文档](https://www.postgresql.org/docs/current/explicit-locking.html)为并发控制参考。本轮没有移植其他 fork 的多账户认证、AI 自动忽略发现、强制出站标记或可覆盖正式发布资产的流程。
