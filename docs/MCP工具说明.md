# MCP 工具与迁移

原来的 29 项包含过细的索引/NetLog 操作、重复的状态入口，以及只返回固定文档的工具。当前公开 40 项（原 19 项 + 2026-11-26 新增动态分析 21 项），按明确参数区分同一任务的来源或动作；固定说明由 MCP resources 提供。2026-11-13 新增 `cdp_events`，移除含已失效 runtime 分支的 `source_export`（其 `source=local` 分支原本可导出本地明文材料与 `report.json`；现由 `get_app_detail` / `index(action="packages")` 提供信息，不再落盘）。`decrypt_wxapkg` 提供本地离线解密能力（原先认为 V1MMWX 静态不可解，实测可用，见下文）。2026-11-22 新增 `export_mini_program` / `export_status`：从应用宝远端**在线下载任意小程序完整 wxapkg**（与离线 `decrypt_wxapkg` 互补）。

| 当前工具 | 职责 | 原入口迁移 |
|---|---|---|
| `search_mini_programs` | 搜索，`source=online/local` | 在线搜索与 `search_local_mini_programs` 合并 |
| `index` | `action=stats/browse/rebuild/packages` | `index_stats`、`browse_indexed`、`rebuild_index`、`list_packages` |
| `get_app_detail` | 指定 AppID 的包与本地材料信息 | 保留 |
| `runtime_status` | 会话列表；`include_processes=true` 追加运行时和进程诊断 | `runtime_sessions`、`host_status`、`list_running` |
| `launch` | 异步独立启动并返回会话 ID；省略 `hide_window` 时继承 GUI 保存值，显式 `true/false` 可覆盖 | 保留 |
| `close_runtime` | 关闭指定自产会话并清理 | 保留 |
| `login_status` | 当前选中账号的公开状态 | 保留 |
| `cdp_local_targets` | 发现调试目标 | 保留 |
| `cdp_connect` | 连接并缓存 CDP 会话 | 保留 |
| `cdp_evaluate` | 常用表达式求值 | 保留，减少 Agent 拼接协议参数 |
| `cdp_send` | 扩展 CDP 操作，如 `Debugger.enable` | `cdp_enable` 改为对应域的 `*.enable` |
| `cdp_scripts` | 脚本清单（当前 WMPF 未回放 `Debugger.scriptParsed`，清单通常为空）；指定 `script_id` 时按 `offset`/`max_bytes` 分页读取源码 | 合并 `cdp_get_script_source` |
| `cdp_events` | 按方法过滤、游标增量读取 console、异常、网络等 CDP 事件 | 新增；读取不消费缓存 |
| `extract_api_surface` | 指定小程序的静态接口候选 | 保留，与 NetLog 运行期证据区分 |
| `netlog` | `action=discover/analyze`，分析同时返回摘要与接口 | `netlog_find_existing`、`netlog_summarize`、`netlog_api_surface` |
| `decrypt_wxapkg` | 离线解密本机 V1MMWX 包并还原工程目录（新增） | 原无对应能力 |
| `export_mini_program` | 提交在线导出任务（appid/username），立即返回 `task_id` | 新增；与离线 `decrypt_wxapkg` 互补 |
| `export_status` | `action=status/list/cancel`，查询导出任务进度与结果 | 新增 |
| `cloud_capture.install/list/replay` | 运行期 `wx.cloud.callFunction` 捕获与显式重放 | 新增 |
| `userscript.add/list/remove/enable/disable/reload` | 持久 Userscript 管理与 CDP 重注入 | 新增 |
| `wxapi.capture/list/replay/delete` | 分类捕获 `wx.request`、storage、native bridge，显式重放与批量删除 | 新增 |
| `route.list/navigate/guard` | 路由清单、安全导航链与跳转守卫 | 新增 |
| `scan.rules/run/report` | 自定义扫描规则、目录扫描与 HTML 报告 | 新增 |
| `package.resolve` | 解析本地包路径、来源与版本元数据 | 新增 |

`decrypt_wxapkg` 是**异步任务**：`action=start` 建任务后立即返回 `task_id`（实测 0.05s），CLI 在后台线程执行，因此不占用 MCP 会话；随后用 `action=status`（省略 `task_id` 取最近任务）轮询进度，`action=list` 看最近任务，`action=cancel` 终止。状态同时写内存和 `data/desktop/decrypt/<task_id>/status.json`，stdio 多进程与 HTTP 单进程都能按 `task_id` 查询。

`decrypt_wxapkg` 的 `appid` 可省略：省略时使用当前活动会话的 `active_appid`。二跳打开的未收录小程序已经下载到 runtime-profile，可直接 `runtime_status → package.resolve → decrypt_wxapkg`，不需要先 `export_mini_program`。`source=auto` 扫描本机运行态包，`source=runtime_profile` 仅使用运行态包；返回包含 `version`、`source_mode`、`package_sources`。

依赖 `vendor/KillWxapkg`（Ackites/KillWxapkg v2.4.1，MIT；本地 fork 仅在 `internal/progress` 等位置加了进度埋点，见该目录 `NOTICE`），可用 `WMPF_WXAPKG_TOOL` 指向其它构建产物。解密密钥由 **appid 派生**，appid 必须与包匹配；工具用 wxapkg 头 `0xBE` 校验解密产物，appid 错误时终态为 `blocked` 而非伪成功。输入包先复制到输出目录下的临时目录，只处理副本，且**不接触任何运行中的微信/应用宝进程**（CLI 只用 `-id/-in/-out/-save/-restore/-pretty`）。

进度与 ETA 分两级：`phase`/`done_packages`/`current_file`/`current_file_bytes` 是 confirmed（来自 fork 输出的 `PROGRESS` 结构化事件，含文件级美化进度）；`eta_seconds` 只在有足够样本时给 `candidate` 级估算并附 `eta_basis`，样本不足、进入还原阶段或正在美化超大文件时返回 `null` 并说明原因。

`out_dir` 可传绝对路径，不同 Agent 各传自己的目录即可隔离；默认 `out/wxapkg/<appid>/<version>`。`pretty` 默认关闭，超过 2MB 的脚本会自动跳过（实测该工具美化 6MB 级 JS 会卡住）。仅限分析本人所有或已获授权的包。

`export_mini_program` / `export_status` 是**在线导出**：通过 `wxapkg-dump` sidecar 用本地授权会话从应用宝远端按 `appid`（或 `username`）下载完整 `wxapkg`，可选 `version_type`（0 正式/1 体验/2 开发/999 预览）、`version`、`all_packages`、`unpack`、`beautify`。它同样是异步任务：提交立即返回 `task_id`，用 `export_status` 轮询。进度是**真实数据**（sidecar 事件流里的 `downloading package n/m`、`reconstructing i/t` 计数），不是估算；`done` 时 `result` 含 `out_dir`/`unpacked_dir`/`packages`。它与 `decrypt_wxapkg` 的区别：后者解本机已有的包，前者从远端拉新包。仅限导出本人所有或已获授权的包。

共 4 个资源：三份协议说明 `wmpf://reference/sdk`、`wmpf://reference/relay`、`wmpf://reference/netlog`（对应原 `sdk_abi_notes`、`xweb_relay_contract`、`netlog_trigger_spec`），以及 `wmpf://status`（运行时进程与配置诊断）。当前能力状态以本文件与 README 为准。

不再注册旧工具别名，因此旧客户端缓存的工具名需要更新。旧禁用项会保守迁移到合并后的工具。目录版本 3 会移除已退休的 `source_export`、`dump_source`、`dump_source_via_cdp` 禁用项，不会将其迁移为 `decrypt_wxapkg` 或其他工具的禁用项。源码入口使用 `decrypt_wxapkg`；本地材料信息使用 `get_app_detail`、`index(action="packages")`；已上报脚本使用 `cdp_scripts`。包级内部导出函数仍保留，但不再作为 MCP 工具公开。

## 示例

```text
search_mini_programs {"keyword":"美团","source":"online","limit":10}
search_mini_programs {"keyword":"wx","source":"local","limit":10}
index {"action":"stats"}
launch {"appid":"真实搜索结果的AppID"}
launch {"appid":"真实搜索结果的AppID","seconds":3600}
runtime_status {}
cdp_local_targets {}
cdp_connect {}
cdp_send {"method":"Debugger.enable"}
cdp_send {"method":"Runtime.enable"}
cdp_send {"method":"Network.enable"}
cdp_events {"since":"now"}
cdp_evaluate {"expression":"6*7"}
cdp_events {"method_filter":["Runtime.consoleAPICalled","Runtime.exceptionThrown","Network.*"],"since":"上一次 next_cursor","limit":50,"wait_ms":1000}
cdp_scripts {}
cdp_scripts {"script_id":"从 cdp_events stackTrace 取到的 scriptId","offset":0,"max_bytes":12000}
cdp_scripts {"script_id":"同一个 scriptId","offset":12000,"max_bytes":12000}
decrypt_wxapkg {"action":"start","appid":"wx141bfb9b73c970a9","version":"509"}
decrypt_wxapkg {"action":"status","task_id":"start 返回的 task_id"}
decrypt_wxapkg {"action":"start","appid":"wx141bfb9b73c970a9","out_dir":"<export-dir>"}
decrypt_wxapkg {"action":"list"}
decrypt_wxapkg {"action":"cancel","task_id":"start 返回的 task_id"}
close_runtime {"session_id":"启动返回的会话ID"}
```

省略 `seconds` 或传 `0` 时会话不按时间自动结束；设置至少 60 的秒数后会在到期时清理。等待会话报告 `debug_connected=true` 再连接 CDP。`Page.getFrameTree` 仍明确拒绝，本次工具增删不改变引擎兼容性边界。

当前 WMPF 调试代理不会重放 `Debugger.scriptParsed`，因此 `cdp_scripts` 返回 `count: 0` 或按 URL 查不到脚本不表示 CDP 请求链断开，也不表示取不到源码——**清单为空只影响“枚举”**。已知 `scriptId` 时 `Debugger.getScriptSource` 可用（实测 `WAPCAdapterGameIndex.js` 1.56MB、`https://usr/game.js` 638KB）；`scriptId` 从 `cdp_events` 的 `Runtime.consoleAPICalled.stackTrace.callFrames[]` 取得，需先 `cdp_send` 发送 `Runtime.enable` + `Debugger.enable`。读取时用 `offset`/`max_bytes` 分页（单脚本可达数 MB，一次全量会超出 MCP 响应上限）。离线还原完整工程目录仍用 `decrypt_wxapkg`。`cdp_events` 不能恢复目标未发送的历史事件。游戏页还会嵌在包装页的 iframe 中，顶层 `wx`/`GameGlobal` 可能是 `undefined`；可在 `cdp_evaluate` 中从 `document.querySelectorAll('iframe')[0].contentWindow` 取游戏上下文，例如：

```js
(() => { const w = document.querySelectorAll('iframe')[0].contentWindow; return JSON.stringify({wx: typeof w.wx, game: typeof w.GameGlobal}); })()
```

## CDP 事件读取

`cdp_events(method_filter, limit=50, since="", wait_ms=0, max_bytes=32000)` 使用当前 `cdp_connect` 连接。不会隐式启用事件域或发送调试命令；先用 `cdp_send` 显式启用 `Runtime.enable`、`Network.enable` 等。事件覆盖范围取决于目标运行时，不能据此保证 worker/AppService/所有网络均被覆盖。

- `method_filter` 为字符串数组，支持精确方法名和 `Network.*` 这样的域前缀，最多 32 项；省略、null 或空数组读取所有事件，不支持正则。
- `since` 省略或空字符串从最早保留事件读。`"now"` 立即返回当前游标，不返回历史事件，即使传了 `wait_ms` 也不等待。随后使用返回的 `next_cursor` 增量读取。
- 游标绑定本次 CDP 连接，重连或未来游标返回 `ok=false, code="cursor_invalid"`；不是时间戳。独立调用者各自保存游标，读取不删除事件。
- 事件按本机接收顺序排列，含 `seq`、`received_at`（Unix 秒）和原始 CDP `method/params/sessionId`。接收顺序不保证不同 CDP 域之间的业务因果顺序。
- `next_cursor` 位于本次扫描末尾，非匹配事件也会推进游标；更换过滤条件需要使用适当的旧游标或省略 since，才能回看先前被过滤掉的事件。
- `has_more=true` 时用 `next_cursor` 继续读，不能改用 `latest_cursor`，否则会跳过分页事件。它只表示本次快照内还有匹配事件，不代表后续不会再有异步事件。
- `gap=true` 与 `missed_events` 表示显式游标后的事件已被缓存淘汰；缺失数量包括所有方法，不能推断其中有多少符合过滤条件。`buffer` 返回保留数量、序列化字节数、累计淘汰/裁剪数量及容量。
- `connection_state="closed"` 表示连接已关闭，仍可读取已保留历史。`ok=false, code="not_connected"` 表示从未连接。空列表本身不表示运行时不支持该事件。
- `limit` 为 1..200，`wait_ms` 为 0..3000。没有匹配事件时可以短等待，收到匹配事件、出现缺口或连接关闭立即返回。
- `max_bytes` 为 2048..40000，限制完整结果对象的 ASCII JSON 序列化大小（不含 MCP 协议外层及文本/结构化结果副本）。超过预算的后续匹配事件留到下一页；单条过大则返回 `truncated=true`、`original_bytes` 与 `preview_json`，其中 params 为空，预览不保证是完整 JSON。游标仍推进，避免永远卡在同一条大事件。

缓存限制为 4096 条 / 8 MiB 序列化数据；单条最大 16 KiB，超限使用显式预览。实际 Python 对象开销高于序列化字节数，但数量与数据体积均受限。脚本元数据独立保留最多 4096 条 / 4 MiB；`cdp_scripts.index` 提示索引淘汰情况，因此普通事件淘汰不会清空脚本清单。

读取对象日志时不会自动展开 objectId；用 `Runtime.callFunctionOn` 等命令按需取值。网络事件用 requestId 关联，收到 ExtraInfo 不等于完整请求或响应正文；正文仍需要对应 CDP 命令并验证运行时是否支持。`cdp_send` 继续只返回命令结果，异步事件通过 `cdp_events` 读取。

更新后需重启相应 MCP 服务进程并刷新 tools/list；stdio 与 GUI 管理的 HTTP 服务分别加载自己的代码。无需为事件读取启动官方程序。
