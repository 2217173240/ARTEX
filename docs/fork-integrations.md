# 同源功能集成

2026-10-19 以 `7856b4b5853ad227da41b77188635508479c8a08` 为基线，继续适配四项有明确使用价值的能力：

| 来源与归属 | 当前采用范围 |
| --- | --- |
| [RC-CHN/ARTEX · `d2b11ab`](https://github.com/RC-CHN/ARTEX/commit/d2b11ab3674f426a6f9044ce96be40c6736a1a0a)，作者 **RuochenPan / RC-CHN**，AGPL-3.0 | 固定 Norma v0.4.3 上独立适配流式命令输出、分页读取、搜索分配和取消处理。原始 upstream 哈希与补丁许可保留；未引入来源的文件 CAS、远程 Runner 或整套应用。具体兼容边界见 [Norma 补丁记录](../third_party/norma/PATCHES.md)。 |
| [BaaRai/ARTEX PR #102](https://github.com/BaaRai/ARTEX/pull/102)，head `283348ef8573da36db79e699b9ac0e5324953a91`，作者 **BaaRai**，AGPL-3.0 | 按当前账本、任务归属和归档模型实现任务结果卡及 UI。继续完善真实复测历史的归档恢复、运行竞态与会话归属；没有把来源的 benchmark 声明当作已验证收益。计量范围见[任务结果说明](task-outcomes.md)。 |
| [jkl2825/artex-evidence-gate · `6603fb51`](https://github.com/jkl2825/artex-evidence-gate/commit/6603fb51d5e2c4894d9a858c8fb1cf8348c1731a)，作者 **jkl2825**，AGPL-3.0 | 独立实现 observation→资格记录→fact 的可追溯链，接入现有执行记录与 CAS 流量快照、检索、展开、冷压缩和归档。资格限定来源记录完整性，业务语义保持未知；不移植来源的通用 HTTP 执行器、baseline 复用或 thread/planner 架构。来源为独立导入根，未证明共同 Git 祖先。见[证据说明](evidence-provenance.md)。 |
| [RuoJi6/ARTEX PR #17](https://github.com/RuoJi6/ARTEX/pull/17)，head `c594efd2b14bfda4e2c067896879e57e618a8830`，作者 **adeljck**，AGPL-3.0 | 借鉴离线包思路，按本 fork 已验证发布镜像、OCI 身份、私有输入排除和现有构建入口独立实现。来源 PR 关闭且未合并；本 fork 的实际双架构验收分别记录，不继承来源自述。见[离线部署说明](offline-deployment.md)。 |

性能对比使用真实固定文件与命令，结果仅适用于记录的场景。任务、来源、语义和归档的新边界使用隔离 PostgreSQL 验证；离线安装使用无外网、空镜像仓库的真实 Docker 环境。新功能无需先被描述为已有缺陷，采用依据是明确收益、架构契合及可验证的维护成本。

采用时保留来源作者与许可；改写部分及本 fork 的兼容、安全和恢复完善另行说明。原有来源监测的事件游标没有因本次功能集成而推进，元数据枚举仍不代表全部源码审计。

2026-10-19 借鉴 [ylh440104/ARTEX · `04569b54`](https://github.com/ylh440104/ARTEX/commit/04569b54bc5fcd419bdefb3da2e2136f2f7ff8c5) 的错误响应防护线索，作者 **ylh440104**，精确提交许可证为 AGPL-3.0。来源是导入快照，未能核实共同 Git 祖先；保留具体提交和许可证证据，没有整体复制历史。

改动前，认证后的文件、数据库和网络错误可经统一写入器直接返回内部诊断。按 [OWASP 错误处理指引](https://cheatsheetseries.owasp.org/cheatsheets/Error_Handling_Cheat_Sheet.html) 独立实现：意外的 5xx 返回固定通用文本，保留状态码、JSON `error` 字段和普通 4xx 提示；明确编写的配置恢复、服务可用性及部分完成说明使用单独的公开消息入口，不传入原始错误。另在模型代理校验源头移除完整 URL 回显，保留既有代理类型及路由行为。没有采用来源的正则脱敏和 panic 包装器；这不是所有响应、日志或流式消息的通用凭据清理。纯合成回归及固定 HEAD 的 CI 用于验证本轮边界，不代表已发生匿名凭据泄露。

2026-10-17 适配 mhtsec/ARTEX 的凭据正文边界：固定提交 [`2d1e156`](https://github.com/mhtsec/ARTEX/commit/2d1e156da4d17e1c48dad457ae62697eb1aef285) 的读取时限与收尾，结合前序 [`2402535`](https://github.com/mhtsec/ARTEX/commit/2402535a9bcef55eb6d7f11fa74d182647ea751c) 的 4 KiB 上限和单 JSON 完整性思路，作者 **mhtsec**，AGPL-3.0。共同初始祖先为 `0ef8561411dd1757e41ba3c45d1436e1fd3774f1`，精确提交的许可证和来源证据保留在任务本地监测记录。

本地合成请求复现了原入口接受超大正文和第二段 JSON、慢速正文持续占用读取的问题。按当前认证架构独立封装三个凭据接口的正文保护：入口立即建立大小与读取时限，完整解码和 EOF 后才恢复连接复用；限流、数据库、身份、跨源与 Basic Auth 的提前拒绝也处理未读正文。保留未知小字段兼容、初始化原子写入、密码 CAS、已有会话撤销和 cookie 契约。实现依据 [Go MaxBytesReader](https://pkg.go.dev/net/http#MaxBytesReader) 与 [ResponseController.SetReadDeadline](https://pkg.go.dev/net/http#ResponseController.SetReadDeadline) 的实际传输语义，验证区分内存 recorder 与真实 HTTP/1、HTTP/2 服务。测试入口只在显式设置隔离 `ARTEX_PG_DSN` 时探测数据库并获取套件锁，纯内存回归避免启动时无条件探测应用配置中的数据库；完整数据库套件仍需显式提供隔离 fixture。

本轮只采用这项有当前代码重叠且可复现的改进；来源的数据库会话重构、远程 Runner/资源库、DrostBench 适配及无当前调度分支重叠的日历修复分别保留为候选。源码元数据枚举、提交抽样与具体差异审阅的范围在本地记录中分别说明。

2026-10-16 的后续完善以 `fe1a38c9a7abe14a3056867fdd86006e65bfa66d` 为基线，优先采用本地可复现的正确性与网络路由改进：

| 来源与归属 | 本 fork 的适配 |
| --- | --- |
| [sehoon123/ARTEX · `bec89a8`](https://github.com/sehoon123/ARTEX/commit/bec89a8f04b9adf6200f99b4df497617e01e3aa2)、[`9ea3cf6`](https://github.com/sehoon123/ARTEX/commit/9ea3cf6c158d260b86721bf58ecc01d3c4b05e69)，作者 **SH Jung / sehoon123**，AGPL-3.0；审阅 source head `3ce884060268d5670e2d3a0429beb24b91380fd2` | 原转发代码仅设置环境变量，合成代理实测未收到请求而目标被直连。使用兼容 Node 22.12 的锁定 Undici 与独立 dispatcher，并要求 Chromium 对回环目标走显式代理；保留默认 TLS 验证和明确的 `insecureTLS` 开关，没有采用源实现的无条件证书跳过。 |
| [RuoJi6/ARTEX PR #14](https://github.com/RuoJi6/ARTEX/pull/14)，head `6a3d39e969ee6f450194cb7408c4db8f191da3f8`，作者 **ji ruo / RuoJi6**，AGPL-3.0 | 只借鉴无效代理应报错的窄切片。非法代理 URL 在本地更新 fixture 中曾意外直连，现于建立客户端时校验并向两个更新入口传播脱敏错误；不增加独立更新代理设置。 |
| [RC-CHN/ARTEX · `05a86185`](https://github.com/RC-CHN/ARTEX/commit/05a861856c69ddbbf4b904680ea984ca2757519f)，AGPL-3.0，作者 **RuochenPan / RC-CHN**，补丁范围见 [Norma 补丁记录](../third_party/norma/PATCHES.md) | 合成工具证实流中断后未闭合参数会被修复成 `{}` 并真正执行。丢弃未提交输入，保留已提交调用的结果与对话配对，中断后不继续启动排队工作，不自动重放工具；按当前执行器生命周期适配。实际成功、实际错误、结果未知及未执行使用不同回执，审批与工具钩子共享该轮取消范围。 |
| [mhtsec/ARTEX #9](https://github.com/mhtsec/ARTEX/issues/9)、[#10](https://github.com/mhtsec/ARTEX/issues/10)、[#11](https://github.com/mhtsec/ARTEX/issues/11)、[#12](https://github.com/mhtsec/ARTEX/issues/12) 的公开问题线索（报告者 **rahul05ranjan**） | 在当前代码逐项复现后独立修复目标 IPv4/端口边界、未知等级排序、UTF-8 文件名截断和证据围栏。问题中旧 server 文件名帮助函数在当前基线不存在，因此只修复仍存在的报告及目标提取路径；未复制未核实的历史实现。 |

上述代码来源的共同初始祖先和 exact-head AGPL 许可证据保留在任务本地监测记录；这些是有界提交与问题审阅，不代表全部 fork 的完整代码审计。代理传输遵循 [Undici ProxyAgent](https://github.com/nodejs/undici/blob/v7.30.1/docs/docs/api/ProxyAgent.md) 与 [Go Transport](https://pkg.go.dev/net/http#Transport) 的契约；证据围栏依据 [CommonMark fenced code blocks](https://spec.commonmark.org/0.31.2/#fenced-code-blocks)。

采集模板需将 `runtime_harvest.js`、`runtime_transport.js`、`package.json` 与锁文件一起复制并执行 `npm ci`。浏览器仅支持无需认证的 HTTP/HTTPS/SOCKS5 代理；携带 userinfo 的代理在启动前明确拒绝，避免把代理凭据发送给目标站点认证挑战。全局环境配置仍按脚本既有优先级选择单个代理，不增加按主机例外或代理失败直连策略。合成 HTTP、HTTPS/CONNECT 和现有 Chromium 回环测试验证本次路由；不代表真实目标授权或生产网络验收。

流中断修复针对明确流错误或取消；没有重新定义正常 EOF 或输出长度截断语义。忽略 context 的工具仍可能继续外部操作，终止回执不能证明副作用撤回。目标提取遇到第一个无效数字地址时拒绝，避免从该地址的端口后缀重新提取另一个域名。

审批模型全错误重试、自主排队取消、独立更新代理设置、额外语言和远程 Runner 继续保留为独立候选，未因出现新提交而自动扩大产品范围。

2026-10-16 适配 [RuoJi6/ARTEX PR #13](https://github.com/RuoJi6/ARTEX/pull/13) 的 HTTP 尝试限流（固定提交 [`bfc5e623`](https://github.com/RuoJi6/ARTEX/commit/bfc5e6236e7197868f2ed8d0598101eb3de7dc0b)，作者 **ji ruo / RuoJi6**，AGPL-3.0）。共同初始祖先为 `0ef8561411dd1757e41ba3c45d1436e1fd3774f1`；围绕当前架构改写，不整包合入源提交。原有 SDK 在逻辑调用前计数，连接和空响应重试不占新额度，分钟令牌桶还会在同一滚动窗口补充额度。现在每次 HTTP 传输准入都检查两个窗口，缓存重建保留同配置的历史；保留现有调用超时、会话头、捕获、额度识别、故障转移与缓存版本保护，不增加 Norma 依赖补丁。

模型页的每秒与每分钟限速同时生效，`0` 关闭对应窗口。例如 `10/s`、`45/min` 表示任意滚动一秒最多 10 次、滚动一分钟最多 45 次 HTTP 尝试，失败尝试和 SDK 重试也计数；不会把未使用额度累积到下一窗口。大于等于 1 的小数向下取整，小于 1 的正数表示延长单次间隔，例如 `0.5/min` 为两分钟一次。配置变更保留已经准入的历史并唤醒等待者；取消等待不预占未来额度，等待时间包含在已有整次调用超时内。无效、非有限或超出可表示范围的值在保存前拒绝。

范围是**同一进程内的同一持久模型配置**，全局环境配置使用独立历史；多个配置即使共用一个上游账号也各自计数，多实例和重启不共享历史。持久配置快照过期、读取或构建失败时，热更新返回错误并保留已有运行状态，不将其重新解释为独立环境配置；没有可用持久配置时保留环境变量启动路径。这不构成所有配置写入和全局安装的完整事务。它限制 HTTP 传输准入，不保证远端到达时间、请求完成并发数或 FIFO 顺序，调整限速不撤回已经准入的请求。为限制高流量下的内存，按小于 10ms 的区间保守合并记录，最多额外等待不足 10ms，每配置约保留 6,002 个时间桶；单进程遇到的配置历史保留到退出。传输遵循 [Go RoundTripper 的并发与请求体关闭约定](https://pkg.go.dev/net/http#RoundTripper)，等待遵循 [context 取消语义](https://pkg.go.dev/context)。本地合成 HTTP 和虚拟时间回归覆盖 45/min、10/s、双窗口、三种协议的流式/非流式及重试、配置重建、取消和过期缓存构建；不以此声称真实上游账号的额度验收。

本轮还核对了 RuoJi6 PR #12 的模型审批重试：已有 SDK 覆盖建连及部分空响应恢复，审批流中断仍有独立恢复空间，但源实现重试全部错误和无效裁决，增加预算、配置和审计成本。缺少实际收益证据时暂缓，不引入新的审批策略。RC-CHN 的排队意图取消属于新增自主规划能力；现有人工任务控制已可移除排队工作，因此暂缓。sehoon123 的 api-recon 代理转发在该轮暂缓，现已按上面的后续复现与适配处理；独立更新代理、额外语言及离线工具包也分别保留证据与范围，不将部分提交取样称为全部代码审计。

2026-10-15 借鉴 [RuoJi6/ARTEX PR #11](https://github.com/RuoJi6/ARTEX/pull/11) 的五类设置删除确认（固定 head `c6ea8ee6a4d8e430aeb540c0c99c43d98c9fa7dc`，实际作者 **adeljck**，AGPL-3.0），按当前组件和中英文独立改写。对应源提交为 MCP [`e336dba`](https://github.com/adeljck/ARTEX/commit/e336dbaeda505e0a11d1dc4384e400dff18c4ce5)、自定义工具 [`34e1083`](https://github.com/adeljck/ARTEX/commit/34e1083158f0001e59f11d3f1e7086827b836d66)、通知 [`aec6e7b`](https://github.com/adeljck/ARTEX/commit/aec6e7bb0d93c36d59980a1f4fb36a860a1b6ca4)、拦截 [`9d8f49a`](https://github.com/adeljck/ARTEX/commit/9d8f49a120a210b6c822e9c3fed55c9ef874691b)、模型 [`5d954f8`](https://github.com/adeljck/ARTEX/commit/5d954f88fc297c2aa9b28a241fd00d92d9b144a1)。PR base 到 head 的祖先关系已核对；未证明候选与当前 main 的共同祖先，因此不宣称直接 cherry-pick。

删除前说明目标和影响：MCP 同时删除 Agent 可见性；通知同时删除投递记录；模型删除可能调整已有任务的配置链和回退；拦截删除影响后续规则匹配。工具配置删除不承诺立即停止已经运行的调用。只有确认才提交，取消与初始焦点采用安全选项，提交中防重入，失败保留可重试对话框；保留默认模型、内置工具及后端保护。复用 [Radix AlertDialog 的受控异步关闭](https://www.radix-ui.com/primitives/docs/components/alert-dialog#close-after-asynchronous-form-submission)和 [WAI-ARIA Alert Dialog 语义](https://www.w3.org/WAI/ARIA/apg/patterns/alertdialog/)，未为开关和保存增加确认，也未新增依赖。

实际页面验收另外发现通知 mock 缺少 `/notify/meta`，空对象使页面访问 `kinds.find` 时崩溃。围绕当前演示接口补齐完整元数据、明确合成且停用的渠道和合法空投递页；删除只修改内存副本并同步列表/计数，未支持的 `/notify` 写入明确报错。未改动真实后端或发送通知，也不将演示回归当作真实渠道投递验收。

同一验收还复现长渠道名将删除控件推出卡片：1440px 下按钮边界超出卡片，中心命中外层网格，真实鼠标无法点击。仅调整通知卡片的最小内容宽度、名称截断及控件禁止收缩，完整名称保留在提示与确认内容中；没有隐藏控件或改动侧栏偏好。

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

## 服务器环境、便携更新与账本查询：2026-10-20

- 依据 LRTK-CODER 的 [`a83040df`](https://github.com/LRTK-CODER/ARTEX/commit/a83040df135f70d61f303250909596fa88195010)（PR171）适配可执行工具与 stdio MCP 的已知服务器秘密默认屏蔽。复用现有 BashEnv/Env，Monitor 补传现有会话环境；Python 在执行边界过滤继承值，stdio 默认值先于明确配置合并。父进程保留动态 provider、超时与启动配置，HTTP/SSE 认证头维持现有合同。具体名称、明确覆盖和权限边界见 [安全策略](../SECURITY.md)。
- 依据同一作者的 [`84c05557`](https://github.com/LRTK-CODER/ARTEX/commit/84c055578145ab72fcf7a5ee3417b148d79c85d2)（PR161）适配 ZIP 流式上限、独占暂存文件、完整 metadata 替换与最小 smoke 环境。当前最大 v0.4.0 ZIP 约 15 MiB，压缩预算定为 128 MiB；保留 512 MiB 解压预算、HTTPS/代理、校验和、managed 安装限制与 Windows 文件命名。已有待应用版本先完成重启，暂存文件清理限定本次拥有的路径。文件访问使用 [Go CreateTemp](https://pkg.go.dev/os#CreateTemp)，读取边界使用 [io.LimitReader](https://pkg.go.dev/io#LimitReader)；跨文件发布沿用单进程顺序合同。
- 依据 lin9527ya 的 [`c6fddb17`](https://github.com/lin9527ya/BLADE/commit/c6fddb174682e2a00c4d5a7e57d664a0412b9976) 适配非空任务的 token 聚合和记录筛选。复用 usage 已有索引，在旧表补列后建立 records 的非 NULL 任务索引；保留 DB 空串同时匹配 NULL/空串的合同、字面值 `0`、独立复测与历史 usage 留存。真实 PG16 受控 fixture 的查询计划确认可使用索引，结果等价；实际部署收益取决于数据量与分布。索引创建采用既有启动迁移方式，大型账本升级应预留建索引时间和磁盘空间，参考 [PostgreSQL 部分索引](https://www.postgresql.org/docs/16/indexes-partial.html)。

三项来源均为 AGPL-3.0，并具有 initial `0ef85614` 的共同祖先；lin 仓库以同一 repository ID 从 ARTEX 改名为 BLADE。采用为适合当前架构的有限行为适配，保留上述作者归属；本地独立 TLS、文件、子进程与 PostgreSQL fixture 验证仅覆盖各自受控合同。

## 公司范围批量读取：2026-10-21

依据 lin9527ya 的 [`758eeddc`](https://github.com/lin9527ya/BLADE/commit/758eeddc3a3b9ed4d8e5b4216ce9c3f98d420460)（AGPL-3.0）适配 `ListCompanies`：先查询公司与资产数量，再按实际列出的 int64 公司 ID 批量读取范围。非空列表由 1+N 次 SQL 调用减少为两次；空公司列表保持一次查询及既有 `null`，公司没有范围时保持 `scope: []`。保留全部八个范围字段、公司名称与范围 ID 顺序、资产数量、nullable logo、负数及超过 int32 的公司 ID，以及数据库读取错误传播。

使用参数化的 [PostgreSQL ANY 数组比较](https://www.postgresql.org/docs/16/functions-comparisons.html#FUNCTIONS-COMPARISONS-ANY-SOME)，沿用当前表、索引与公开接口。真实 PG16 受控实验核对完整 JSON 和单连接执行；部署收益取决于公司数量、访问频率和数据库延迟。两次读取保持既有普通查询合同，不承诺同一数据库快照；范围、共享和资产归属语义沿用当前实现。

## 工具链与看板请求：2026-10-22

- 依据 [RuoJi6/ARTEX PR36](https://github.com/RuoJi6/ARTEX/pull/36) 的候选，按 [Go 官方安全公告](https://groups.google.com/g/golang-announce/c/66pojueaQek) 将根模块最低版本升级为 Go 1.26.10。该版本修复 CVE-2026-111943，对特定受损中间证书公钥执行验证限制；不是拒绝全部 eMudhra 证书。现有质量、原生包及发行工作流通过 `go-version-file` 读取该版本。已发布的安装包、二进制和镜像须重新构建交付后才能获得补丁；绕过证书验证的连接不受此项修复保护。
- 依据 lin9527ya / BLADE 的 [`1fc3f26f`](https://github.com/lin9527ya/BLADE/commit/1fc3f26fb387dbd8855bdd1f9a095aa29198d8ba) 移除看板中没有消费者的 `TokenTotal` 状态和每五秒 `tokenStats` 请求。其他页面的 API、看板的统计来源、刷新周期与 usage 窗口保持现有合同。该改进消除确定的无用请求，实际部署收益取决于看板访问量。

两项采用按当前架构独立适配，来源沿用 AGPL-3.0；保留来源仓库、作者与提议状态，不整体合并其历史。看板 usage 按视图加载、关停收尾以及暂停时长分别需要加载状态、生命周期和计时口径验收，继续作为独立候选。

## 本地镜像构建说明：2026-10-23

依据 jubeater 的 [RuoJi6/ARTEX PR37](https://github.com/RuoJi6/ARTEX/pull/37)（提交 [`30362063`](https://github.com/RuoJi6/ARTEX/commit/303620638f03c3df27afed82714bb10ddf698d62)，AGPL-3.0）适配 Dockerfile 顶部的本地准备命令：先创建 `server/webui/dist`，再将 `web/out/.` 的内容复制到其中。新检出目录可创建缺失的父目录，重复执行会更新同名文件并保持输出目录层级。复制沿用覆盖语义，已经移除的构建产物不会自动清理。

验证在隔离目录执行实际文件复制，覆盖父目录缺失和目标目录已存在两种情况，并检查更新后的文件内容与目录层级。验证范围仅为文档中的目录创建与复制命令；镜像构建步骤与生产镜像层保持现有行为，没有执行前端、Go 或 Docker 完整构建。

## 移动端设置与技能布局：2026-10-23

依据 xingchuan（[chu0119/ARTEX · `6488985a`](https://github.com/chu0119/ARTEX/commit/6488985afa45c91caab62dbace90a85eaa442bdf)，AGPL-3.0）的布局改进，适配当前五个页面：模型、Agent 和工具设置抽屉在手机端使用完整视口宽度，保留桌面宽度；技能目录在窄屏纵向排列，并按当前组件补充文件树独立滚动和编辑区最小高度；流量页标题与状态允许换行。来源独立导入历史的共同 Git 祖先未核实，仅改写现有页面的布局类；当前没有代理池页面，因此未引入对应来源页面。

本地合成数据界面在手机和桌面视口检查，不连接模型或真实业务 API。此项保持现有保存、认证、工具调用与状态逻辑；API 契约解析及技能标签属于其他候选功能，未随布局改动引入。
