# 同源功能集成

2026-10-15 借鉴 [RuoJi6/ARTEX PR #11](https://github.com/RuoJi6/ARTEX/pull/11) 的五类设置删除确认（固定 head `c6ea8ee6a4d8e430aeb540c0c99c43d98c9fa7dc`，实际作者 **adeljck**，AGPL-3.0），按当前组件和中英文独立改写。对应源提交为 MCP [`e336dba`](https://github.com/adeljck/ARTEX/commit/e336dbaeda505e0a11d1dc4384e400dff18c4ce5)、自定义工具 [`34e1083`](https://github.com/adeljck/ARTEX/commit/34e1083158f0001e59f11d3f1e7086827b836d66)、通知 [`aec6e7b`](https://github.com/adeljck/ARTEX/commit/aec6e7bb0d93c36d59980a1f4fb36a860a1b6ca4)、拦截 [`9d8f49a`](https://github.com/adeljck/ARTEX/commit/9d8f49a120a210b6c822e9c3fed55c9ef874691b)、模型 [`5d954f8`](https://github.com/adeljck/ARTEX/commit/5d954f88fc297c2aa9b28a241fd00d92d9b144a1)。PR base 到 head 的祖先关系已核对；未证明候选与当前 main 的共同祖先，因此不宣称直接 cherry-pick。

删除前说明目标和影响：MCP 同时删除 Agent 可见性；通知同时删除投递记录；模型删除可能调整已有任务的配置链和回退；拦截删除影响后续规则匹配。工具配置删除不承诺立即停止已经运行的调用。只有确认才提交，取消与初始焦点采用安全选项，提交中防重入，失败保留可重试对话框；保留默认模型、内置工具及后端保护。复用 [Radix AlertDialog 的受控异步关闭](https://www.radix-ui.com/primitives/docs/components/alert-dialog#close-after-asynchronous-form-submission)和 [WAI-ARIA Alert Dialog 语义](https://www.w3.org/WAI/ARIA/apg/patterns/alertdialog/)，未为开关和保存增加确认，也未新增依赖。

实际页面验收另外发现通知 mock 缺少 `/notify/meta`，空对象使页面访问 `kinds.find` 时崩溃。围绕当前演示接口补齐完整元数据、明确合成且停用的渠道和合法空投递页；删除只修改内存副本并同步列表/计数，未支持的 `/notify` 写入明确报错。未改动真实后端或发送通知，也不将演示回归当作真实渠道投递验收。

同一 PR 的 RAG 和 Makefile 没有整体引入：RAG 的凭据响应、设置校验、来源撤销、网络与输出预算、任务归档契约仍需完整适配；重复构建入口没有替代当前脚本。jiwoochris PR #34 的 MCP 卡片和 PR #30 的报告重试已覆盖，未重复采用；自动侧栏折叠会改变已保存偏好，任务表隐藏列尚无当前缺陷复现，不据此移植。

2026-10-14 另适配 [jiwoochris/artex-global PR #34](https://github.com/jiwoochris/artex-global/pull/34) 的 MCP 卡片部分（最终 head `9fdfd69d0e92ee590e6437ca7b5f04c3568bb93d`，作者 meowassassin，AGPL-3.0）。当前展开侧栏、1100px 视口中的卡片宽度为 183px，启用及删除控件会超出卡片并被裁剪；调整卡片列数断点和传输标签收缩规则后，实际组件的控件边界、鼠标命中及键盘焦点均通过验证。覆盖 320px 中文、1100px 中英文及 1440px 英文；mock 不持久化启用状态，未将其视为服务状态验收。保留现有侧栏偏好、任务表格列和全部配置行为。仓库旧名称 `jiwoochris/artex-ko` 仍作为增量监测键，通过别名记录对应新名称。

2026-10-14 借鉴 [BaaRai PR #92](https://github.com/BaaRai/ARTEX/pull/92)（head `c191b3b6e70ed4c4194d19d0ce2dfa276b8f9443`，作者 BaaRai，AGPL-3.0），补齐任务详情的聚合指标。真实 PostgreSQL 和已认证 API 回归复现列表非零、详情全零的问题；列表和详情现在共用 DTO 填充，详情查询仅聚合当前任务，复用现有 SQL 计数。保留结果活动的 token 口径、目标及运行中意图、内存 heartbeat、归并案例与原始上报的区别、空任务省略字段、归档依赖和现有可降级读取行为。没有引入来源每次查询全部任务的实现。

同轮适配 [RuoJi6 PR #9](https://github.com/RuoJi6/ARTEX/pull/9)（head `b9b137dc914139194a6f0debfb2d1552c79bf525`，作者 ji ruo / RuoJi6，AGPL-3.0）的 HTTP 选中复制修复思路。通过 DOM Range 端点映射原始文本位置，保留 CRLF、LF、单独 CR、连续空行、跨高亮片段及反向选择；仅处理完整位于当前报文内的单段选择。实现遵循 [copy 事件](https://developer.mozilla.org/en-US/docs/Web/API/Element/copy_event)的 `clipboardData.setData` 和取消默认行为语义；跨组件选择、无有效选择或剪贴板事件不可用时保留原行为。隔离 Chrome 挂载实际组件验证修复前后的 DOM 与复制事件，追加 Unicode 和节点边界回归；合成事件验证不等同于操作系统剪贴板验收。

2026-10-13 借鉴 [RuoJi6 的审批检索 `2f2b78c`](https://github.com/RuoJi6/ARTEX/commit/2f2b78cc78ac9ca81c47f67ad45841828571a8a5) 与 [人工裁决来源 `97213e3`](https://github.com/RuoJi6/ARTEX/commit/97213e357b6b49edb3a5c760c235658e2ca30981)（作者 ji ruo / RuoJi6，AGPL-3.0），按当前分页和审批事务适配。全局及任务内审批记录支持内容搜索、UTC 创建日期范围和人工来源筛选；人工决定仍只允许从待审批状态原子提交，超时、取消和执行结果关联沿用现有保障。来源提交中的脚本快照、脚本策略配置和旁路问答布局没有整体移植。

同轮适配 [jiwoochris/artex-ko PR #30](https://github.com/jiwoochris/artex-ko/pull/30)（head `06ae2504d454320b227fd4f623ecf29d269bd223`，作者 jum-apzn，AGPL-3.0）的报告加载状态：读取错误显示可重试提示，成功读取空内容才显示暂无报告；切换任务隔离请求和可复制内容，沿用当前翻译系统。另借鉴 [BaaRai 的 `94efd1c`](https://github.com/BaaRai/ARTEX/commit/94efd1ca316fadd8b0f487a48a638a8a435075ef)（作者 BaaRai，AGPL-3.0），修复新建技能遗漏的 YAML 安全序列化：通过已有 yaml.v3 编码字符串和 MCP 列表，实际技能加载器验证冒号、多行、分隔符和中文往返，保留 Markdown 正文、空值省略及冲突处理；没有复制自定义标量编码器或启动检查机制。

还适配 [BaaRai PR #67](https://github.com/BaaRai/ARTEX/pull/67)（head `8f4dfe0aae12c245df5d7d5fcb2476185edc41f3`，作者 BaaRai，AGPL-3.0）的存储 API Key 归一化：模型配置测试使用存储 Key 时，去除与运行时相同的外围空白。通过本地模拟提供方复现外围换行导致非法请求头，并验证修复、表单值优先级及配置原值保留；没有改动 session header 的现有语义。来源 PR #75 所述只传 profile_id 的测试入口不适用于当前界面调用，端点回填作为独立 API 完整性候选保留。

本 fork 进一步收紧技能创建的文件边界：完整编码在写盘前完成，持有 `os.Root` 并排他创建目录及文件。实际并发请求复现了旧实现两个请求同时成功的问题；现在只有一个返回 201，其他同名请求返回 409，保留成功请求的内容。已有目录、符号链接和无关文件保持，失败清理仅涉及本次创建的文件及空目录，不使用递归删除。

2026-10-12 还吸纳 [jiwoochris/artex-ko PR #25](https://github.com/jiwoochris/artex-ko/pull/25) 的路径规范改进（head `d0c76b2875e68a075b832ded4d019afc6d9d5166`，作者 jaelim8789-glitch，AGPL-3.0）：技能文件 API 的输入、列表及响应始终使用 `/`，仅在文件系统边界使用本机路径。保留全部字符、编码和越界检查；六种原生 runner 直接执行嵌套中文文件的列表、读取、写入、建目录及删除往返回归。未引入来源 PR 中无关的 Python 解释器探测变更。

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

## 审批历史检索

历史列表与任务内列表的 `q`、`created_from`、`created_to` 可与已有状态、来源和分页参数组合。`q` 去除两端空白后最多 200 个 Unicode 字符，按字面内容忽略大小写匹配命令输入、原因、审计信息及仍可用的会话标题、任务字段和规则名称。`%`、`_`、反斜杠按字面字符搜索，采用 [PostgreSQL LIKE 转义规则](https://www.postgresql.org/docs/current/functions-matching.html#FUNCTIONS-LIKE)。`#123` 这样的正整数编号只匹配对应审批 ID。

日期采用 `YYYY-MM-DD`，明确按 UTC 解释，首末日均包含；数据库使用首日零点到末日次日零点的半开区间。开始日期晚于结束日期时返回校验错误。列表计数与内容使用相同过滤，删除任务后仍可检索审批中保留的字段；不会为已缺失的标题或任务文本补造历史快照。

`decision_source=manual` 表示最终人工允许或拒绝，不代表独立用户身份。新的人工决定在同一原子更新中记录来源；旧记录仅在审计中存在准确的“人工允许执行 / 人工拒绝执行”且状态相符时推断。初始规则、模型原因与审计内容继续保留，取消或超时不归为人工操作，缺少旧审计的记录保持原有信息。

## 备注与人工处置

现有认证仅提供一个共享 ARTEX 管理员，备注不承诺逐人审计。正文为 1–8000 个 Unicode 字符；只允许来源上下文修改，继承上下文可查看，归档排队及归档过程中冻结写入。归档恢复保留备注、作者、时间、ID 与最后人工处置信息；内部备注不自动写入公开漏洞报告。

最后人工处置记录保存 actor、时间和当时的状态，状态与记录同事务提交。自动复测或 Agent 改变状态后保留这条历史记录；界面展示其历史状态，不将其视为对新状态的人工认可。重复提交相同状态不刷新记录。

## 实现参考与边界

[MDN SSE 文档](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events)说明了断线重连、事件 ID 与代理缓冲。补取仅在实时连接断开时工作；代理仍应禁用缓冲。页面切换会丢弃进行中的旧补取响应，已有 HTTP 请求可能完成。

备注、归档和任务控制沿用数据库事务与既有锁序；[PostgreSQL 显式锁文档](https://www.postgresql.org/docs/current/explicit-locking.html)为并发控制参考。本轮没有移植其他 fork 的多账户认证、AI 自动忽略发现、强制出站标记或可覆盖正式发布资产的流程。

## 扩展来源筛选：2026-10-14

本轮将发现范围扩大到监测名单以外的同名、改名仓库和较早 fork。GitHub 元数据枚举返回 841 个同名仓库、765 个原始网络 fork；具体审查采用分层提交样本，不代表对全部仓库完成代码审计。

- 借鉴 [6ccg/ARTEX 的 `cb5dfec`](https://github.com/6ccg/ARTEX/commit/cb5dfec8fcc3844bd7d56f3e208ff1cb1a34d4b0)（作者 6ccg），修补当前 `go-mitmproxy v1.9.2` 在 SOCKS5 拨号失败后关闭 nil 连接的问题。官方 `v1.9.3` 仍包含该代码，因此只固定原版本的运行包并应用小补丁，保留原 MIT 许可证、文件来源和校验清单；没有引入来源仓库的整体 vendor、代理架构或 Docker 出站策略。维护及回归方法见 [依赖补丁说明](../third_party/go-mitmproxy/ARTEX-PATCHES.md)，本地模块替换沿用 [Go 官方 replace 机制](https://go.dev/ref/mod#go-mod-file-replace)。
- 借鉴 [wufufu770/reArtex 的 `b85d2d9`](https://github.com/wufufu770/reArtex/commit/b85d2d974111b226b1a1f0291437f19a4ac745fd)（作者 wufufu770），为 `deny/ask` 正则规则增加 JSON 解码后独立字符串值匹配。`allow` 继续仅匹配原始参数，避免无关字段扩大自动放行范围。原始参数仍用于审计和模型复核；保留启用范围、优先级与 first-match，不跨字段拼接，不采用来源的凭据短路表或工具 ask 策略。它补齐等价转义的确定性匹配，不构成完整命令语义沙箱。
- 借鉴 [RC-CHN/ARTEX 的 `9278fb7`](https://github.com/RC-CHN/ARTEX/commit/9278fb7c4066234bdd19729cfec8a94cf14978a1)（作者 RuochenPan / RC-CHN），将工作并发设置同步到已有任务的常规 Worker 池。扩容增加可用循环；缩容立即限制新工作准入，已经执行的工作自然完成。池独立取消并等待动态循环退出，保留任务暂停、终态、删除、归档、deadline、预算与既有 inflight 保障。设置成功保存后读取最新持久值，读取失败保持全部现有池容量；配置查询等待不会占用停止任务需要的锁。若保存后同步失败，返回明确的部分完成错误，用户可重试。任务并发上限与常规 Worker 上限各自独立，已有临时 intervention 路径保持原行为。

三项 ARTEX 来源沿用 AGPL-3.0，底层 MIT 依赖保留其原许可与作者。采用方式为围绕当前架构重写或应用单独依赖补丁，不将 fork 提交认证为缺失的原始 PR。

当时暂缓的 Norma 连续 tool 消息和 MCP 熔断现已按下述边界适配。lin9527ya 的公司知识共享仍需明确授权与来源生命周期。seongilp 的全量 scope overview 与当前按需 `list_companies` 契约冲突；旧 Windows PowerShell 启动器需适配现有初始化令牌和进程监督，不直接移植。M0dred 清除的 postcss 附加混淆内容不存在于当前文件。

## 后续采纳：消息协议、故障隔离与可选工具

- 适配 RuochenPan / RC-CHN 的 [`956abad7`](https://github.com/RC-CHN/ARTEX/commit/956abad73e9f88cd9f4dcc37036af4344bf2f3ba) 与 [`568ad83a`](https://github.com/RC-CHN/ARTEX/commit/568ad83a2e5978ec2546434c832bc6076ae86bce)：压缩摘要置于存活工具交换之外，Session 模型引用使用实际排序；OpenAI 编码器按紧随 assistant 的连续工具结果校验，保留合法反序并行结果、原始历史和合法空结果。官方 Norma v0.4.3 的依赖闭包固定到 [third_party/norma](../third_party/norma/PATCHES.md)，三个消息协议生产文件应用窄补丁，另修补两处容量运算（共四个生产修改路径）；没有整体引入 RC 模块或升级到 v0.4.4。原版本树没有许可证文件，附带官方后续明确的 Apache-2.0 文档并准确记录来源；RC 补丁保留 AGPL-3.0 来源边界。保留测试范围与原版本既有失败分别记录，根 CI 显式执行嵌套模块回归。NOA 仍为默认关闭的实验设置，局部请求复现不等于所有真实任务失败。
- 借鉴 lin9527ya [`24b776e0`](https://github.com/lin9527ya/ARTEX/commit/24b776e09716013b50e4aa2c72e2cc581ba0017c) 的并发与熔断思路，原创实现每个 remote MCP Client 的状态机，没有直接 cherry-pick，也没有声明已证明共同 ancestry。HTTP 与 legacy SSE 共用可取消请求准入；默认最多两个同时请求、五次连续传输故障后冷却一分钟，冷却后仅一个恢复探针。调用者取消、关闭连接及合法 RPC 业务错误不计服务故障；旧 epoch 结果不能清除新的熔断。保留同源、AskUser/Capture 和原有初始化/通知语义；不重放有副作用请求。恢复策略参考 [Microsoft Circuit Breaker pattern](https://learn.microsoft.com/en-us/azure/architecture/patterns/circuit-breaker)，取消遵循 [MCP 生命周期](https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/cancellation)的既有边界，不宣称远程服务器一定停止已开始的操作。
- 围绕当前 Python 自定义工具修复取消与超时的进程树收敛：Unix 使用所属进程组，Windows 在句柄绑定的 Job Object 分配成功后才放行用户脚本，输出管道等待设限。保留正常完成时已有后台行为、JSON 参数、工作目录和环境；这是可实测的生命周期修复，不是 shell ulimit、CPU/RAM 配额或完整沙箱。参考 [Go os/exec](https://pkg.go.dev/os/exec) 和 [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)；六种原生 runner 执行合成 Python 回归。
- 借鉴 RC-CHN [`c65a1cdd`](https://github.com/RC-CHN/ARTEX/commit/c65a1cdd10ad3109cd60d3ca4ee9cf608c2584da)，新增 Nuclei/Vulnx 可选工作流与只读诊断，准确区分情报候选、模板覆盖和验证发现。没有复制预装镜像声明、启动播种、自动下载或强制默认启用；沿用当前技能分发与用户修改/删除保留规则。使用说明见 [可选安全工具](security-tools.md)。

远程 Runner/预热涉及当前依赖尚无的 ExecutionRuntime、租约与文件一致性，需要独立部署和真实容器验收；本轮不加入不可用骨架。公司全量知识共享没有替代现有显式、直接、实时过滤删除来源的只读继承。完整资源配额、默认公司共享及分布式执行没有实现，也不以这次回归作为它们已完成的证据。
