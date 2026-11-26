# Frida 可重链接材料与重建说明（LGPL-2.1 义务）

本项目通过 `native/worker/wmpf-observer.exe` 静态链接 **Frida core / Gum 17.2.17**（LGPL-2.1-or-later），该 worker 随后被 `go:embed` 内嵌进 `wmpf-gui.exe`。本文档提供 LGPL-2.1 第 6 条所要求的**对应源码获取方式、构建配方与可重链接目标文件生成方式**，使使用者能够在修改 Frida 后重新链接出新的可执行文件。

> 本项目当前仅分发源码，不分发预编译二进制。因此下列材料是"若自行构建并分发二进制时"履行 LGPL 义务所需；使用者自行构建仅供个人使用时，LGPL 的分发义务不触发。

## 1. 版本与来源（对应源码）

| 组件 | 版本 | 对应源码 |
|---|---|---|
| Frida core | 17.2.17 | https://github.com/frida/frida-core/tree/17.2.17 |
| Frida Gum | 17.2.17 | https://github.com/frida/frida-gum/tree/17.2.17 |
| Frida（总仓库） | 17.2.17 | https://github.com/frida/frida/tree/17.2.17 |
| 预编译 devkit | 17.2.17 windows-x86_64 | https://github.com/frida/frida/releases/download/17.2.17/frida-core-devkit-17.2.17-windows-x86_64.exe |

devkit 归档 SHA-256（校验后再解压，切勿运行未校验的归档）：

```
5f6a5170629dcec99e2958f42b4e35c79abef2d16e9fd3627a3f900b54005477
```

devkit 内含 `frida-core.h`、`frida-core.lib` 及捆绑依赖（含 GLib，头文件声明为 LGPL-2.1-or-later）。

## 2. 应用侧源码（本项目自有部分）

需要重新链接的应用代码完整包含在本仓库中：

- `native/worker/main.cpp` —— observer 全部 C++ 源码
- `tools/observe_startup_chain.js`、`tools/activate_reused_runtime.js` —— 被编译期内嵌为 C++ 字符串常量的 agent 脚本
- `native/worker/build.ps1` —— 完整构建配方（生成 `agents.h`、调用 `cl.exe`、链接 `frida-core.lib`）
- `native/cmd/workbench-gui/` —— GUI + MCP 单 exe 的 Go 源码与前端（`go:embed` 内嵌上述 worker）

## 3. 重建 / 重链接步骤

### 3.1 使用官方 devkit 重建（默认路径）

```powershell
# 1) 校验并解压 devkit 到 build/deps/frida-core-17.2.17
#    下载 URL 与 SHA-256 见本文档第 1 节
#    解压后目录应包含 frida-core.h 与 frida-core.lib

# 2) 编译 observer（生成中间目标文件 build/worker/worker.obj，
#    并链接为 build/wmpf-observer.exe）
powershell.exe -NoProfile -ExecutionPolicy Bypass -File native\worker\build.ps1

# 3) 把新 observer 拷入 bundle 输入并重建单 exe
Copy-Item build\wmpf-observer.exe native\internal\bundle\bin\ -Force
powershell.exe -NoProfile -ExecutionPolicy Bypass -File native\build-release.ps1
```

### 3.2 修改 Frida 后重新链接（LGPL 核心场景）

若你修改了 Frida core/Gum（例如打补丁），需要自行从源码编译出 windows-x86_64 的 `frida-core.lib`，替换 devkit 中的同名文件，再执行上述 3.1 的第 2、3 步。这样即可在不改动本项目应用代码的前提下，把修改后的 LGPL 库重新链接进最终可执行文件。

可选：`build.ps1` 的 `-Devkit` 参数可指向任意自定义 devkit 目录，便于指向你自行编译的版本：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File native\worker\build.ps1 -Devkit <你的 devkit 目录>
```

### 3.3 可重链接目标文件

`build.ps1` 中的 `cl.exe /Fo"$build/worker/worker.obj"` 会生成应用侧的目标文件 `worker.obj`。它是应用代码与 Frida 库之间的可重链接边界：保留该文件后，可仅用 `link.exe` 配合不同版本的 `frida-core.lib` 重新链接，无需重新编译 `main.cpp`。`build/worker/` 属于构建输出，被 `.gitignore` 排除；按 3.1 重新构建即可生成。

## 4. 许可证文本

LGPL-2.1 及其捆绑依赖的许可证文本应从上游获取并随分发物一并提供：

- Frida core / Gum（LGPL-2.1-or-later）：https://github.com/frida/frida/blob/main/COPYING
- GLib（LGPL-2.1-or-later，devkit 捆绑）：https://gitlab.gnome.org/GNOME/glib/-/blob/main/COPYING
- 完整依赖清单以 `native/worker/README.md` 与 `docs/THIRD_PARTY_NOTICES.md` 为准

## 5. 边界说明

- 独立进程形态**不解除** LGPL 义务：静态链接的 `wmpf-observer.exe` 仍受约束，且其被内嵌进 GUI exe 时义务随最终分发物传递。
- 不得对第三方库代码或其可重链接目标文件施加反逆向保护。
- 本项目不分发微信运行时 DLL；它们由使用者本机提供，与 Frida 义务相互独立。
