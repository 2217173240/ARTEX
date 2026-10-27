# 按会话诊断模型用量

轻量调用账本保存任务、exploration、Agent、会话、模型、配置名、耗时和提供方返回的 token 数。关闭请求与响应正文录制后，该账本继续写入；会话标识用于关联调用，不改变任务归属或工具权限。

## 查询接口

`GET /api/llm/usage` 使用现有管理员登录认证。启用 HTTP Basic Auth 时，同时满足 Basic Auth 和管理员登录要求。

| 参数 | 含义 |
| --- | --- |
| `task` | 必填，规范的正整数任务注册表 ID；允许查询已删除任务保留的账本 |
| `session` | 可选，精确匹配会话标识；显式空值筛选历史未知会话 |
| `page` | 从 0 开始，默认 0 |
| `size` | 默认 50，允许 1–200 |

无效任务 ID、分页值或上述参数重复时返回 400。数据库不可用返回 503，读取失败返回现有错误响应。

使用已登录会话的 cookie 文件查询；任务和会话值替换为实际标识：

```sh
curl --get --cookie cookies.txt \
  --data-urlencode 'task=42' \
  --data-urlencode 'session=conv-123' \
  'http://127.0.0.1:8787/api/llm/usage'
```

省略 `session` 可先查看该任务的调用，再按返回的 `session_id` 筛选。响应包含 `records`、`total`、`page`、`size`；空结果为 `records: []`。每条记录包含 `id`、`ts`、`task_id`、`exploration_id`、`worker`、`session_id`、`model`、`profile_name`、`latency_ms`、`input_tokens`、`output_tokens`、`cache_read`、`cache_write`、`status`，不含请求与响应正文。

记录按写入 ID 倒序排列。单次响应的总数与记录来自一致的只读快照；不同分页请求之间，追加、归档等操作可能改变结果。

## 历史和归档

历史缺失的会话标识返回空字符串，不作推测回填。提供方未返回 usage 时，账本仍沿用现有零值；该值无法证明调用没有消耗。会话第一条可见记录也不能证明它是首次调用。

删除任务或正文录制记录后，轻量账本及其会话关联继续作为历史计量保留。完整任务归档沿用 v4 格式，保存会话字段；旧包缺失该字段仍可恢复。此接口查询热账本，归档中的逐条调用需要还原后查询。全局汇总、任务结果卡和活动会话统计保持各自现有口径，见[任务结果度量](task-outcomes.md)。
