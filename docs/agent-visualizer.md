# LLM Gateway Visualizer

在 LLM Gateway 设置中打开 **LLM Gateway Visualizer**，或从首页左下角菜单打开独立窗口。窗口以 WebView 加载内嵌文件 `/web/index.html`，跟随 Copilot Chat 的 Light/Dark 设置。它与 GoVisual 的 HTTP request inspection 独立开关；关闭 Visualizer 会清空本次采集。

## 轮次与分支

`internal/agentviz` 在客户端协议边界观察 Messages、Responses 和 Chat Completions。请求保持原样转发，跨协议转换后的响应按客户端实际收到的格式采集。

- 一个方块汇总一次用户/调度器轮次中的 HTTP 请求。工具结果和系统插入消息延续当前轮次。
- 返回当前轮次尚在等待的工具 ID，表明工具循环正在继续。该批结果旁边的 Skill 正文、环境说明或中途排队输入保留在原轮次的注入消息中，不据此判为新指令中断。已消费或无关的工具 ID、显式用户中断标记不能作为正常续接的证据。
- `end_turn` 后的 Stop hook 反馈、后台任务通知可以继续原轮次。显式 cron 标记在空闲时开启 scheduler 轮次。
- 工具块决定是否等待工具结果。中断流、客户端断开、HTTP 错误和输出截断各保留失败尝试；重试成功另占一格。
- 会话 ID 优先取请求头，兼容 Claude Code 的 `metadata.user_id`。ID 作为线索：SDK 更换 ID 后，只要完整的可见历史继续扩展已观察到的进度，仍沿用原行。中断尝试留在该行，改写历史才建立 rewind 分支。实际并行的旧客户端重新出现时，将新 ID 的分支后缀拆回子行，保留原主行和轮次 ID。显式 Agent ID / parent-agent ID、subagent 标记会直接隔离子代理。
- 用于匹配的历史忽略助手 reasoning 块的有无与分组，完整比较用户指令、工具 ID、参数、结果和媒体摘要。原始签名、加密 reasoning 与转发正文保持原样；相同指令预览或高缓存命中率不会单独触发合并。
- Claude Code 当前指令带有完整的 `[SUGGESTION MODE: …]` 用途标记时，记录到独立的 `Prompt suggestion` 临时 fork。它共享历史前缀，拥有自己的请求进度；主会话不会跟随其输出推进。即使先观察到 suggestion，也为同一个 wire session 保留主会话身份，收到真实请求后再显示主行并补齐可观察的共享前缀锚点。
- 历史消息保存在共享前缀树中。新会话共享已观察到的助手历史、或同一会话 rewind 后分叉时，新行只记录新工作，连线指向最后共享轮次。
- 已识别的 compaction 和工具结果裁剪保留原会话。匹配忽略缓存标记，保留工具 ID、参数、数字精度与图片摘要。
- Claude Code 和 Claude Desktop 放在 `messages` 中的 `# Environment` 系统消息会显示在轮次详情的 **Environment** 区块，位于 **System** 和 **Tool definitions** 之间。展开后显示原文，包括同一段内的 `# MCP Server Instructions`。

Gateway 只能看到 HTTP 协议。没有来源标记的纯文本 scheduler 请求可能与人类输入完全相同；此时按普通输入显示。开始采集前的历史用于匹配，不补造历史轮次。

## 压缩时间轴

同一会话的轮次依次占列。重叠会话选择距离最近的现有列，同一行不会合并两个轮次。只有一个起始轮次时，后来会话可在三分钟宽限期内对齐；已经形成的时间轴之后出现的新活动继续向右扩展。

列之间的空闲按前序轮次结束时间计算：超过 3、10、30 分钟分别插入 1、2、3 个空格。主会话与执行中、等待工具的 fork，以及最近两个已结束或失败的 fork 一起移动；其余旧 fork 按自身最后一轮开始时间独立排序，保留 fork 标记并隐藏连线。旧 fork 再有请求时会重新归组。分组只影响展示，原始血缘与共享前缀保持不变。排序变化通过动画呈现。

已完成轮次按缓存命中率分为四档绿色：低于 75%、75% 至低于 90%、90% 至低于 95%、95% 及以上。灰白色只表示空格。

点击会话名选择最新轮次；点击矩阵空白取消选择。悬浮提示显示该轮次的用户指令。fork 连线从父方块朝向子行的上/下边缘中点出发，经过一次直角转折，进入目标方块左侧中点。

## 数据与安全

- 所有采集只在进程内存中，重启后消失。默认保留预算为 64 MiB、4,096 次轮次尝试和 256 行；以空闲分支族为单位回收。正在传输的请求可暂时超出预算，后续采集暂停至回收完成。
- 相同内容与历史前缀共享。图片等二进制内容保存长度和 SHA-256 摘要；超大文本显示裁剪标记。工具参数在详情 API 中以 JSON 文本传给浏览器，避免 JavaScript 舍入大整数。
- 轮次总 Token 为各请求的输入与输出之和。未缓存输入包括新输入和缓存写入；命中率为累计缓存读取 / 累计输入。首 chunk 时间从轮次开始计时。
- 原生窗口每秒通过只读 MyGo 接口检查一次状态，版本未变时返回空快照。版本包含采集代数，开关或 Gateway 重启不会复用旧状态。接口校验调用页面身份，其他 WebView 无法读取采集内容。
- loopback 调试页面保留同源访问限制，支持 ETag/304。不包含 Gateway 密钥，不加载外部资源。提示词、工具结果通过文本节点呈现。

验证：`go test ./internal/agentviz ./internal/localproxy ./internal/desktop`、`node --test internal/agentviz/web/visualizer.test.mjs`。设置 `AGENTVIZ_CAPTURE_FILES` 为本机 GoVisual 导出的路径列表后，`TestLocalSuggestionCaptures` 与 `TestLocalEarlierSuggestionTimeline` 离线回放完整 suggestion/main 请求和响应。截断记录可通过 `AGENTVIZ_INTERRUPTED_CAPTURES` 检查已保存的完整消息前缀及 session ID 变化，并用明确构造的中断/编辑尾部验证归属；缺失的原始尾部不作完整回放。抓包内容不加入仓库、不发送上游。
