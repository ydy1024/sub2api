# PopGo 视频任务

PopGo 使用 OpenAI-compatible 的三段式异步视频接口：

- `POST /v1/videos` 创建任务
- `GET /v1/videos/{task_id}` 查询状态
- `GET /v1/videos/{task_id}/content` 下载视频

Base URL 为 `https://api.popgo.site/v1`，请求使用同一个 `Authorization: Bearer <API_KEY>`。

## 账号配置

账号需要配置为：

- 平台：`openai`
- 类型：`apikey`
- Base URL：`https://api.popgo.site`
- 凭据：`video_api_style: openai_videos`

网关会把创建请求转发到上游 `/v1/videos`，查询和下载分别使用
`/v1/videos/{task_id}` 与 `/v1/videos/{task_id}/content`。任务绑定按用户、
Key 和任务 ID 隔离，状态查询不会切换到其他账号。

管理后台的账号新建/编辑页提供“OpenAI 兼容视频协议”开关。历史上使用
`grok` 平台承载同一协议时保存的 `video_api_style` 仍然兼容，但新账号应使用
`openai` 平台。

## 调用示例

```bash
curl http://127.0.0.1:18080/v1/videos \
  -H "Authorization: Bearer YOUR_SUB2API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MX-SD-2.5-B",
    "prompt": "河南老君山雪景，金顶云海日出",
    "seconds": "10",
    "resolution": "720p",
    "aspect_ratio": "16:9"
  }'
```

模型参数以现场文档为准。例如 `MX-SD-2.5-B` 的 `seconds` 必须是字符串，
范围为 10–30 秒，并且需要显式提供 `resolution` 和 `aspect_ratio`。

状态查询响应中的 `video_url` 或 `metadata.url` 会被重写为本地
`/v1/videos/{task_id}/content`，客户端不需要也不应该把 Sub2API Key
发送到上游媒体 URL。

## 计费与恢复

标准计费模式要求为模型配置视频每秒价或渠道按次价；不再将图片价格作为视频每秒价格。请求须显式提供正数 `seconds`/`duration` 和 `resolution`/`size`，避免不同供应商默认值导致错扣。JSON 与 multipart 均支持 `seconds`，超过 15 秒的时长保留实际值。

创建前在数据库保存 `video_billing_tasks` 并预留余额，成功后先保存上游任务 ID 再返回响应。后台每 15 秒扫描待处理任务，查询到完成状态后使用创建时保存的价格结算；客户端无需持续轮询。并发 worker 和重复查询共享数据库幂等键。明确失败或取消会释放预留；余额用完仍能查询和下载自己已提交的任务。

升级需要执行迁移 `250_video_billing_tasks.sql`（由项目启动迁移器执行）。已存在的 Redis 任务仍走原查询结算兼容路径；升级前未保存到数据库的任务无法自动恢复。

上游创建请求超时、5xx 或创建结果 ID 保存失败时，无法确定是否已产生成本。该任务保留 `submitting` 状态与预留金额，记录内部任务 ID，不自动重新生成或退款。运维需先核对上游；确认任务 ID 后可将对应记录绑定到 `task_id` 并转为 `pending` 交给后台结算，确认未创建才能释放预留。不要直接删除尚有预留的任务行。
