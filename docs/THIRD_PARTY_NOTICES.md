# 第三方组件与许可清单（发行前核对）

> 本清单基于 `native/go.mod`、`native/gui`（已合并到主模块）、`native/worker/` 与仓库内第三方目录整理。
> **发行前必须逐项附上完整许可证文本；静态链接的 LGPL 组件还需提供可重链接产物与对应源码。**
> 本文件不是法律意见，也不代表已完成 VMP 或发行验收。

## 1. 自研组件（本仓库）

| 组件 | 说明 | 许可 |
|---|---|---|
| `native/cmd/workbench-gui` | 单 exe：Go + WebView 桌面界面，内嵌 MCP 服务（stdio / HTTP） | 随项目 |
| `native/worker/wmpf-observer.exe` | C++ Frida 观测 worker | 随项目（链接 Frida，见下） |
| `vendor/KillWxapkg` 离线入口 | wxapkg 离线解密/解包 | MIT（上游） |
| `hostcpp/wmpfhost_integrated.exe` | 自产 C++ 宿主 | 随项目 |

## 2. Go 依赖（`native/go.mod`）

| 模块 | 用途 | 许可 |
|---|---|---|
| `github.com/wailsapp/wails/v2` | 桌面窗口/绑定（仅 GUI 二进制链接） | MIT |
| `github.com/wailsapp/go-webview2` | WebView2 绑定 | MIT |
| `github.com/gorilla/websocket` | CDP / 桥 WebSocket | BSD-3-Clause |
| `github.com/modelcontextprotocol/go-sdk` | MCP 协议 | MIT |
| `modernc.org/sqlite`（含 libc/mathutil/memory） | 纯 Go SQLite（索引/登录服务复用） | BSD-3-Clause |
| `github.com/google/uuid` | ID 生成 | BSD-3-Clause |
| `golang.org/x/sys`、`golang.org/x/exp` | Windows API / 工具 | BSD-3-Clause |
| `github.com/go-ole/go-ole` | COM（WebView2 链路） | MIT |
| `github.com/jchv/go-winloader` | Windows 动态加载 | MIT |

完整传递依赖以 `native/go.sum` 为准；发行前用 `go list -m all` 与各模块 `LICENSE` 生成逐项文本。

## 3. 原生/第三方

| 组件 | 版本/来源 | 许可与义务 |
|---|---|---|
| Frida core / Gum | 17.2.17，静态链接进 `wmpf-observer.exe` | LGPL-2.1-or-later。静态链接需提供对应源码、构建配方与可重链接的应用目标文件；隔离进程不解除义务 |
| Wails（前端运行时部分） | v2.10.2 | MIT |
| WebView2 Runtime | 系统组件（本机 157.0.4341.46） | Microsoft 分发条款；**不随精简包分发** |
| `vendor/KillWxapkg` | 上游 MIT 项目 | MIT；保留版权与许可文本 |
| [wxapkg-dump](https://github.com/RYF5584/wxapkg-dump) | 上游 MIT 项目，作为独立 sidecar 进程（`wxapkg-dump.exe serve`）运行 | MIT；保留版权与许可文本。**随精简包分发**：构建时嵌入主程序，首次运行解压到 `data/bin/wxapkg-dump.exe`；也可用 `WXAPKG_DUMP_EXE` 指向自备副本 |
| 微信运行时（WeChatAppEx/WeChatAppEx.exe、flue.dll、pcsdk 等） | 用户本机提供，来源为应用宝 PC 的 `WmpfRuntime\5.10.7300.414\` | 不随包分发；离线包只允许拷贝经校验的 Runtime，并遵守其条款 |

## 4. 发行形态与义务

- **精简版（自研程序）**：单一 `wmpf-gui.exe`（内嵌观测 worker、离线解密 worker、自产宿主与 MCP 服务）+ 本清单与许可证文本；不包含 Runtime、`data/`、`out/`、研究工具与源码。
- **离线版**：在精简版基础上包含经 SHA-256 校验的 Runtime 目录；体积显著增大，需单独测量。
- Frida 静态链接：对应源码获取、构建配方、可重链接目标文件生成与重建步骤见 [FRIDA_RELINK.md](FRIDA_RELINK.md)。LGPL 义务在分发二进制时触发；本项目当前仅分发源码。
- VMP：**未安装、未验证**，不得声称已加固。

## 5. 已知未完成项

- Frida 可重链接与对应源码指引已见 `FRIDA_RELINK.md`；其余逐项完整许可证文本与第三方源码包仍未随仓库打包。
- 未在无 Go/MSVC/Frida 开发工具的机器上验证。
- 未完成整个进程树 RSS/启动时间/磁盘体积的发行级测量。
- 未做 VMP POC，也未做保护前后的签名核验。
