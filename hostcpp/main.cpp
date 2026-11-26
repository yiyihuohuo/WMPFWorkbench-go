// WMPF 独立宿主（C++）
// 阶段：加载 host DLL -> 伪造线程环境 -> IBC -> QI -> handler -> 登录 -> LaunchApplet
#include <windows.h>
#include <tlhelp32.h>
#include "window_lifecycle.h"

#include <cstdint>
#include <cerrno>
#include <cstdlib>
#include <cstdio>
#include <cstring>
#include <string>
#include <utility>
#include <vector>

// ---------------- 路径与常量 ----------------
static std::wstring g_runtimeW;
static std::string g_runtimeA;
static std::string g_dataDir = "data";
// 可参数化：对照实验里通过 WMPF_ROOT 换引擎数据目录
static std::string g_rootDir = "data\\runtime-profile";
static std::string g_loginBufFile = "yyb_login_buffer_live.txt";
static std::string g_launchCfgFile = "yyb_launch_config_run9.bin";
static bool g_noPatch = false;    // WMPF_NO_PATCH=1：撤掉 +0x92/jne 两个 patch
static bool g_useHandler = false; // WMPF_USE_HANDLER=1：传自建 handler 而不是 NULL
static bool g_useCb = false;      // WMPF_USE_CB=1：登录链传真实回调（官方有传）
static std::int64_t g_obsSec = 0; // WMPF_OBS=<秒>，0/省略表示不限时
static bool g_obsKeep = false;    // WMPF_OBS_KEEP=1：跑满 obs，不因 renderer 提前退出
static const char* kAppId = "wx141bfb9b73c970a9";
static std::string g_appId;
static HANDLE g_loginReady = nullptr;

static std::int64_t ObservationSeconds(const char* value) {
    if (!value || !*value) return 0;
    char* end = nullptr;
    errno = 0;
    const auto seconds = std::strtoll(value, &end, 10);
    return errno == ERANGE || end == value || *end || seconds < 0 ? 0 : seconds;
}

static bool ObservationPending(ULONGLONG began, ULONGLONG now, std::int64_t seconds) {
    return seconds == 0 || (now - began) / 1000 < static_cast<ULONGLONG>(seconds);
}

static const uint32_t kTlsIdxRva = 0x378820;
static const uint32_t kSlot1 = 0x158;
static const uint32_t kSlot2 = 0x160;

typedef void* (*GetBrowsingService_t)();
typedef uint64_t (*Fn2)(void*, void*);
typedef uint64_t (*Fn3)(void*, void*, void*);
typedef uint64_t (*Fn4)(void*, void*, void*, void*);
typedef uint64_t (*Fn5)(void*, void*, void*, void*, void*);
typedef long long (*CbT)(void*, void*, void*, void*);

// ---------------- handler 对象（512B + 64 槽 vtable，逐槽记录） ----------------
template <int N>
static long long SlotStub(void* self, void* a1, void* a2, void* a3) {
    printf("[hdl] slot=%d tid=%lu self=%p a1=%p a2=%p a3=%p\n", N,
           GetCurrentThreadId(), self, a1, a2, a3);
    fflush(stdout);
    return 0;
}

static void* g_vt[64];
static struct {
    void** vt;
    void** vt2;
    uint8_t pad[512 - 16];
} g_handler;

template <size_t... I>
static void FillSlots(std::index_sequence<I...>) {
    ((g_vt[I] = (void*)(uintptr_t)&SlotStub<(int)I>), ...);
}

static void* MakeHandler() {
    FillSlots(std::make_index_sequence<64>{});
    memset(&g_handler, 0, sizeof(g_handler));
    g_handler.vt = g_vt;
    g_handler.vt2 = g_vt;
    return &g_handler;
}

// ---------------- 线程环境伪造 ----------------
static void* StubThunk(void*, void*, void*, void*) { return nullptr; }

struct Controller {
    void* vtable[8];
    uint64_t refcount;
};
struct Holder {
    void* controller;
    void* pad0;
    void* pad1;
};
static Holder g_h1, g_h2;
static Controller g_c1, g_c2;

static bool EnsureThreadEnv(HMODULE mod) {
    uint32_t idx = *(uint32_t*)((uint8_t*)mod + kTlsIdxRva);
    void** tlsp = (void**)(uintptr_t)__readgsqword(0x58);
    if (!tlsp) return false;
    uint8_t* blk = (uint8_t*)tlsp[idx];
    if (!blk) return false;
    for (int i = 0; i < 8; ++i) {
        g_c1.vtable[i] = (void*)(uintptr_t)&StubThunk;
        g_c2.vtable[i] = (void*)(uintptr_t)&StubThunk;
    }
    g_c1.refcount = 0x10000;
    g_c2.refcount = 0x10000;
    g_h1.controller = &g_c1;
    g_h2.controller = &g_c2;
    *(void**)(blk + kSlot1) = &g_h1;
    *(void**)(blk + kSlot2) = &g_h2;
    printf("[env] tls_index=%u block=%p\n", idx, blk);
    return true;
}

// ---------------- CreateProcessW IAT hook（补 wmpf_root_dir） ----------------
typedef BOOL(WINAPI* CreateProcessW_t)(
    LPCWSTR, LPWSTR, LPSECURITY_ATTRIBUTES, LPSECURITY_ATTRIBUTES, BOOL, DWORD,
    LPVOID, LPCWSTR, LPSTARTUPINFOW, LPPROCESS_INFORMATION);

static CreateProcessW_t g_realCPW = nullptr;
static std::wstring g_extraCmd;
static int g_cdpPort = 0;      // ★ E57：DLL 自己带 --wmpf_root_dir 后，
                               //   g_extraCmd 的整段追加会被跳过，
                               //   所以 CDP 端口要在 DetourCPW 里单独补。
static int g_cpwHits = 0;

// ---------------- child exit-code watcher (diagnostic) ----------------
struct WatchedProcess {
    HANDLE handle;
    DWORD pid;
};

static DWORD WINAPI WatchProc(LPVOID p) {
    auto* watched = static_cast<WatchedProcess*>(p);
    DWORD waitResult = WaitForSingleObject(watched->handle, INFINITE);
    DWORD code = 0;
    if (waitResult == WAIT_OBJECT_0 && GetExitCodeProcess(watched->handle, &code)) {
        printf("[cpw-watch] pid=%lu child exit code=0x%08X (%u)\n",
               watched->pid, code, code);
    } else {
        printf("[cpw-watch] pid=%lu wait/query failed error=%lu\n",
               watched->pid, GetLastError());
    }
    fflush(stdout);
    CloseHandle(watched->handle);
    delete watched;
    return 0;
}

static BOOL WINAPI DetourCPW(LPCWSTR app, LPWSTR cmdline,
                             LPSECURITY_ATTRIBUTES pa,
                             LPSECURITY_ATTRIBUTES ta, BOOL inherit,
                             DWORD flags, LPVOID env, LPCWSTR dir,
                             LPSTARTUPINFOW si, LPPROCESS_INFORMATION pi) {
    std::wstring cmd = cmdline ? cmdline : L"";
    // product-id 必须是 3000（PC 小程序模式），否则后续管理器不初始化
    size_t p = cmd.find(L"--product-id=");
    if (p == std::wstring::npos) {
        cmd += L" --product-id=3000";
    } else {
        size_t e = p + 14;
        while (e < cmd.size() && iswdigit(cmd[e])) ++e;
        cmd = cmd.substr(0, p) + L"--product-id=3000" + cmd.substr(e);
    }
    // ★ wechat-sub-user-agent 补值（12:13 成功基线的一部分，无害，保持与已验证状态一致）
    if (cmd.find(L"WeChatAppEx") != std::wstring::npos &&
        cmd.find(L"MicroMessenger") == std::wstring::npos) {
        const std::wstring uaFlag = L"--wechat-sub-user-agent";
        size_t u = cmd.find(uaFlag);
        if (u != std::wstring::npos) {
            size_t v = u + uaFlag.size();
            size_t e2 = v;
            while (e2 < cmd.size() && cmd[e2] == L' ') ++e2;
            bool emptyVal = (e2 >= cmd.size() || cmd[e2] == L'-');
            if (emptyVal) {
                cmd.insert(v, L" MicroMessenger/3.7.6(0x03070006) WindowsWechat");
                printf("[cpw] UA injected after --wechat-sub-user-agent\n");
            }
        }
    }
    if (!cmd.empty() && !g_extraCmd.empty() &&
        cmd.find(L"--wmpf_root_dir") == std::wstring::npos) {
        cmd += g_extraCmd;
    }
    // ★ E57：无论 g_extraCmd 是否被追加，CDP 端口都单独补（只对 WeChatAppEx）
    if (g_cdpPort > 0 && cmd.find(L"WeChatAppEx") != std::wstring::npos &&
        cmd.find(L"--remote-debugging-port") == std::wstring::npos) {
        wchar_t buf[80];
        swprintf(buf, 80, L" --remote-debugging-port=%d --remote-allow-origins=*",
                 g_cdpPort);
        cmd += buf;
    }
    ++g_cpwHits;
    printf("[cpw#%d] %ls\n", g_cpwHits, cmd.substr(0, 300).c_str());
    fflush(stdout);
    BOOL result = g_realCPW(app, cmd.empty() ? nullptr : &cmd[0], pa, ta, inherit,
                           flags, env, dir, si, pi);
    DWORD createError = GetLastError();
    // PROCESS_INFORMATION is an output: inspect it only after successful creation.
    if (result && pi && pi->hProcess) {
        HANDLE h2 = nullptr;
        if (DuplicateHandle(GetCurrentProcess(), pi->hProcess,
                            GetCurrentProcess(), &h2, 0, FALSE,
                            DUPLICATE_SAME_ACCESS)) {
            auto* watched = new WatchedProcess{h2, pi->dwProcessId};
            HANDLE thread = CreateThread(nullptr, 0, WatchProc, watched, 0, nullptr);
            if (thread) {
                CloseHandle(thread);
            } else {
                CloseHandle(h2);
                delete watched;
            }
        }
    }
    SetLastError(createError);
    return result;
}

// 在目标模块的导入表里把 CreateProcessW 换成 DetourCPW
static bool HookIatCreateProcessW(HMODULE mod) {
    auto* base = (uint8_t*)mod;
    auto* dos = (IMAGE_DOS_HEADER*)base;
    auto* nt = (IMAGE_NT_HEADERS*)(base + dos->e_lfanew);
    auto& dir = nt->OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT];
    if (!dir.VirtualAddress) return false;
    auto* desc = (IMAGE_IMPORT_DESCRIPTOR*)(base + dir.VirtualAddress);
    for (; desc->Name; ++desc) {
        const char* dll = (const char*)(base + desc->Name);
        if (_stricmp(dll, "KERNEL32.dll") != 0 &&
            _stricmp(dll, "kernel32.dll") != 0) {
            continue;
        }
        auto* oft = (IMAGE_THUNK_DATA*)(base + desc->OriginalFirstThunk);
        auto* ft = (IMAGE_THUNK_DATA*)(base + desc->FirstThunk);
        for (; oft->u1.AddressOfData; ++oft, ++ft) {
            if (IMAGE_SNAP_BY_ORDINAL(oft->u1.Ordinal)) continue;
            auto* imp = (IMAGE_IMPORT_BY_NAME*)(base + oft->u1.AddressOfData);
            if (strcmp((const char*)imp->Name, "CreateProcessW") != 0) {
                continue;
            }
            g_realCPW = (CreateProcessW_t)(uintptr_t)ft->u1.Function;
            DWORD old = 0;
            VirtualProtect(&ft->u1.Function, sizeof(void*), PAGE_READWRITE,
                           &old);
            ft->u1.Function = (uintptr_t)&DetourCPW;
            VirtualProtect(&ft->u1.Function, sizeof(void*), old, &old);
            printf("[cpw] IAT hooked: real=%p\n", (void*)g_realCPW);
            return true;
        }
    }
    printf("[cpw] CreateProcessW import not found\n");
    return false;
}

// ---------------- CreateNamedPipeW IAT hook（观测 mojo 管道创建时序） ----------------
// 官方在 GetUserInfo 后会新建一条到 BROWSER 的 auth 管道；宿主这条是硬差分点。
typedef HANDLE(WINAPI* CreateNamedPipeW_t)(LPCWSTR, DWORD, DWORD, DWORD, DWORD,
                                           DWORD, DWORD,
                                           LPSECURITY_ATTRIBUTES);
static CreateNamedPipeW_t g_realCNP = nullptr;
static int g_pipeHits = 0;

static HANDLE WINAPI DetourCNP(LPCWSTR name, DWORD openMode, DWORD pipeMode,
                               DWORD maxInst, DWORD outSize, DWORD inSize,
                               DWORD timeout, LPSECURITY_ATTRIBUTES sa) {
    HANDLE h = g_realCNP ? g_realCNP(name, openMode, pipeMode, maxInst, outSize,
                                     inSize, timeout, sa)
                         : INVALID_HANDLE_VALUE;
    ++g_pipeHits;
    if (name && wcsstr(name, L"mojo")) {
        printf("[pipe#%d] h=%p %ls\n", g_pipeHits, h, name);
        fflush(stdout);
    }
    return h;
}

static bool HookIatCreateNamedPipeW(HMODULE mod) {
    auto* base = (uint8_t*)mod;
    auto* dos = (IMAGE_DOS_HEADER*)base;
    auto* nt = (IMAGE_NT_HEADERS*)(base + dos->e_lfanew);
    auto& dir = nt->OptionalHeader.DataDirectory[IMAGE_DIRECTORY_ENTRY_IMPORT];
    if (!dir.VirtualAddress) return false;
    auto* desc = (IMAGE_IMPORT_DESCRIPTOR*)(base + dir.VirtualAddress);
    for (; desc->Name; ++desc) {
        const char* dll = (const char*)(base + desc->Name);
        if (_stricmp(dll, "KERNEL32.dll") != 0 &&
            _stricmp(dll, "kernel32.dll") != 0) {
            continue;
        }
        auto* oft = (IMAGE_THUNK_DATA*)(base + desc->OriginalFirstThunk);
        auto* ft = (IMAGE_THUNK_DATA*)(base + desc->FirstThunk);
        for (; oft->u1.AddressOfData; ++oft, ++ft) {
            if (IMAGE_SNAP_BY_ORDINAL(oft->u1.Ordinal)) continue;
            auto* imp = (IMAGE_IMPORT_BY_NAME*)(base + oft->u1.AddressOfData);
            if (strcmp((const char*)imp->Name, "CreateNamedPipeW") != 0) {
                continue;
            }
            g_realCNP = (CreateNamedPipeW_t)(uintptr_t)ft->u1.Function;
            DWORD old = 0;
            VirtualProtect(&ft->u1.Function, sizeof(void*), PAGE_READWRITE,
                           &old);
            ft->u1.Function = (uintptr_t)&DetourCNP;
            VirtualProtect(&ft->u1.Function, sizeof(void*), old, &old);
            printf("[pipe] CreateNamedPipeW IAT hooked: real=%p\n",
                   (void*)g_realCNP);
            return true;
        }
    }
    printf("[pipe] CreateNamedPipeW import not found\n");
    return false;
}

// ---------------- 登录回调（官方传了真实回调；宿主原来传 NULL） ----------------
static int g_cbHits = 0;

struct AuthState {
    volatile LONG gotUserInfo;
    int32_t status;
    uint32_t type;
    uint64_t uin;
    int32_t scene;
    volatile LONG gotLogin;
    int32_t loginType;
    int32_t loginError;
    int respLen;
};
static AuthState g_authState = {};

static void ResetAuthState() {
    InterlockedExchange(&g_authState.gotUserInfo, 0);
    InterlockedExchange(&g_authState.gotLogin, 0);
    g_authState.status = -1;
    g_authState.type = 0;
    g_authState.uin = 0;
    g_authState.scene = 0;
    g_authState.loginType = 0;
    g_authState.loginError = 0;
    g_authState.respLen = 0;
}

static void OnGetUserInfoTyped(int32_t status, uint32_t type, uint64_t uin,
                               int32_t scene, void* data) {
    auto* state = static_cast<AuthState*>(data);
    if (state) {
        state->status = status;
        state->type = type;
        state->uin = uin;
        state->scene = scene;
        InterlockedExchange(&state->gotUserInfo, 1);
    }
    printf("[authcb] GetUserInfo status=%d type=%u uin=%llu scene=%d data=%p\n",
           status, type, (unsigned long long)uin, scene, data);
    fflush(stdout);
}

static void OnLoginTyped(int32_t type, int32_t error, const char* resp_pb,
                         int resp_len, void* data) {
    auto* state = static_cast<AuthState*>(data);
    if (state) {
        state->loginType = type;
        state->loginError = error;
        state->respLen = resp_len;
        InterlockedExchange(&state->gotLogin, 1);
    }
    printf("[authcb] Login type=%d error=%d resp=%p len=%d data=%p\n",
           type, error, (const void*)resp_pb, resp_len, data);
    fflush(stdout);
}

// AppletEvent / JsApi handler：实测 SetAppletEventHandler 的入参被存进 mgr+0x50，
// 随后被**直接当函数指针 call**（传对象 → DEP 0xc0000005 at 对象地址）。
// 所以这里按函数指针形态提供。
static int g_evtHits = 0;

static long long OnAppletEventFn(void* a0, void* a1, void* a2, void* a3,
                                 void* a4, void* a5, void* a6) {
    ++g_evtHits;
    printf("[evt#%d] tid=%lu a0=%lld a1=%lld a2=%p a3=%p a4=%p a5=%p\n",
           g_evtHits, GetCurrentThreadId(), (long long)a0, (long long)a1, a2,
           a3, a4, a5);
    fflush(stdout);
    return 0;
}

static long long OnAuthCb(void* a0, void* a1, void* a2, void* a3, void* a4,
                          void* a5, void* a6, void* a7) {
    ++g_cbHits;
    printf("[authcb#%d] tid=%lu a0=%p a1=%p a2=%p a3=%p a4=%p a5=%p a6=%p\n",
           g_cbHits, GetCurrentThreadId(), a0, a1, a2, a3, a4, a5, a6);
    fflush(stdout);
    // The callback return value can affect whether the DLL treats the user as
    // valid. Keep the default neutral and allow explicit diagnostics overrides.
    if (const char* rv = getenv("WMPF_AUTH_RET")) return _atoi64(rv);
    return 0;
}

// ★ E46 实证：官方 LaunchApplet 的第 5 参数是 **事件回调**（exe 内 0x7ff6cac912d0），
//   不是窗口句柄。宿主一直传 hwnd/nullptr -> DLL 无法注册 applet 事件 ->
//   applet 容器（xweb）不启动 -> 不读 codecache/wxapkg -> JS 不执行 -> 100ms 断开。
static long long OnAppletEvent(void* a0, void* a1, void* a2, void* a3,
                               void* a4, void* a5, void* a6, void* a7) {
    // 官方签名（0x170EB0 里的字符串常量）：
    //   web_object::WXApplet::LaunchApplet::<lambda_1>::operator()(const char*, int, ...)
    //   第一个参数是描述串，第二个是 resultCode（官方日志 "[FROM SDK] launch result ... 0 OK"）
    char msg[256] = {0};
    const char* pmsg = "(null)";
    if (a0) {
        __try {
            strncpy_s(msg, sizeof(msg), (const char*)a0, 255);
            pmsg = msg;
        } __except (EXCEPTION_EXECUTE_HANDLER) { pmsg = "(bad ptr)"; }
    }
    printf("[applet_ev] code=%d msg=%s\n", (int)(intptr_t)a1, pmsg);
    // 抓 101 的生成栈，定位 host DLL 内决定 resultCode 的位置
    {
        void* frames[12];
        USHORT nf = CaptureStackBackTrace(0, 12, frames, nullptr);
        printf("[applet_bt] n=%u modBase=%p\n", (unsigned)nf,
               (void*)GetModuleHandleA("wmpf_host_export_x64.dll"));
        for (USHORT i = 0; i < nf; ++i)
            printf("   #%u %p\n", (unsigned)i, frames[i]);
        fflush(stdout);
    }
    // DLL 可能以返回值判断"回调已处理，可继续"。默认返回 1，可用 WMPF_EV_RET 覆盖。
    if (const char* rv = getenv("WMPF_EV_RET")) return _atoi64(rv);
    return 1;
}

static long long OnAuthSetCb(void* a0, void* a1, void* a2, void* a3, void* a4,
                             void* a5) {
    printf("[setcb] tid=%lu a0=%p a1=%p a2=%p a3=%p a4=%p\n",
           GetCurrentThreadId(), a0, a1, a2, a3, a4);
    fflush(stdout);
    return 0;
}

// ---------------- 工具 ----------------
static uint8_t* g_modBase = nullptr;

static LONG WINAPI OnCrash(EXCEPTION_POINTERS* ep) {
    auto* rec = ep->ExceptionRecord;
    uint8_t* addr = (uint8_t*)rec->ExceptionAddress;
    long long rva = g_modBase ? (long long)(addr - g_modBase) : -1;
    printf("[crash] code=0x%08lx addr=%p rva=0x%llx\n", rec->ExceptionCode,
           (void*)addr, (unsigned long long)rva);
    for (DWORD i = 0; i < rec->NumberParameters && i < 2; ++i) {
        printf("   param[%lu]=%p\n", i, (void*)rec->ExceptionInformation[i]);
    }
    fflush(stdout);
    return EXCEPTION_CONTINUE_SEARCH;
}

// 崩溃上下文里不能用 CRT/printf（可能死锁），直接写文件
static void RawLog(const char* text) {
    HANDLE f = CreateFileA((g_dataDir + "\\crash.txt").c_str(),
                           FILE_APPEND_DATA, FILE_SHARE_READ, nullptr,
                           OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (f != INVALID_HANDLE_VALUE) {
        DWORD n = 0;
        WriteFile(f, text, (DWORD)strlen(text), &n, nullptr);
        CloseHandle(f);
    }
}

static LONG CALLBACK OnVeh(EXCEPTION_POINTERS* ep) {
    auto* rec = ep->ExceptionRecord;
    if (rec->ExceptionCode != 0xC0000005 && rec->ExceptionCode != 0x80000003) {
        return EXCEPTION_CONTINUE_SEARCH;
    }
    uint8_t* addr = (uint8_t*)rec->ExceptionAddress;
    long long rva = g_modBase ? (long long)(addr - g_modBase) : -1;
    char buf[256];
    _snprintf_s(buf, sizeof(buf), _TRUNCATE,
                "[veh] code=0x%08lx addr=%p rva=0x%llx tid=%lu depth=%lu\n",
                rec->ExceptionCode, (void*)addr, (unsigned long long)rva,
                GetCurrentThreadId(),
                ep->ContextRecord
                    ? (unsigned long)((CONTEXT*)ep->ContextRecord)->Rsp
                    : 0);
    RawLog(buf);
    printf("%s", buf);
    fflush(stdout);
    return EXCEPTION_CONTINUE_SEARCH;
}

static void Pump(int ms) {
    DWORD end = GetTickCount() + (DWORD)ms;
    MSG msg;
    while (GetTickCount() < end) {
        while (PeekMessageW(&msg, nullptr, 0, 0, PM_REMOVE)) {
            TranslateMessage(&msg);
            DispatchMessageW(&msg);
        }
        Sleep(5);
    }
}

static std::string ReadText(const std::string& name) {
    std::string p = name.size() > 2 && name[1] == ':' ? name : g_dataDir + "\\" + name;
    FILE* f = nullptr;
    if (fopen_s(&f, p.c_str(), "rb") != 0 || !f) return std::string();
    std::string s;
    char buf[8192];
    size_t n;
    while ((n = fread(buf, 1, sizeof(buf), f)) > 0) s.append(buf, n);
    fclose(f);
    while (!s.empty() && (s.back() == '\n' || s.back() == '\r')) s.pop_back();
    return s;
}

static int CountChildren(DWORD parent, const wchar_t* exe) {
    HANDLE snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    if (snap == INVALID_HANDLE_VALUE) return 0;
    PROCESSENTRY32W pe;
    pe.dwSize = sizeof(pe);
    int n = 0;
    if (Process32FirstW(snap, &pe)) {
        do {
            if (_wcsicmp(pe.szExeFile, exe) == 0 &&
                pe.th32ParentProcessID == parent) {
                printf("     child %ls pid=%lu\n", pe.szExeFile,
                       pe.th32ProcessID);
                ++n;
            }
        } while (Process32NextW(snap, &pe));
    }
    CloseHandle(snap);
    return n;
}

static uint64_t ReadU64(void* p, uint32_t off) {
    return *(uint64_t*)((uint8_t*)p + off);
}

// ---------------- 进程命令行 / 祖先链 / renderer 检测 ----------------
// 旧实现只数直接子进程，BROWSER 的 renderer 是孙进程，永远数不到。
typedef LONG(NTAPI* NtQip_t)(HANDLE, ULONG, PVOID, ULONG, PULONG);

static std::wstring ProcCmdline(DWORD pid) {
    static NtQip_t ntqip = (NtQip_t)GetProcAddress(
        GetModuleHandleW(L"ntdll.dll"), "NtQueryInformationProcess");
    if (!ntqip) return std::wstring();
    HANDLE h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, pid);
    if (!h) return std::wstring();
    std::vector<uint8_t> buf(0x4000);
    ULONG ret = 0;
    std::wstring out;
    // 60 = ProcessCommandLineInformation；返回 UNICODE_STRING{len,max,pad,ptr}
    LONG st = ntqip(h, 60, buf.data(), (ULONG)buf.size(), &ret);
    if (st >= 0) {
        USHORT len = *(USHORT*)buf.data();
        const wchar_t* p = *(const wchar_t**)(buf.data() + 8);
        if (p && len > 1 && len < 0x3ffe) out.assign(p, len / sizeof(wchar_t));
    }
    CloseHandle(h);
    return out;
}

struct ProcInfo {
    DWORD pid;
    DWORD ppid;
    std::wstring exe;
};

static std::vector<ProcInfo> SnapshotProcs() {
    std::vector<ProcInfo> v;
    HANDLE snap = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
    if (snap == INVALID_HANDLE_VALUE) return v;
    PROCESSENTRY32W pe;
    pe.dwSize = sizeof(pe);
    if (Process32FirstW(snap, &pe)) {
        do {
            ProcInfo pi;
            pi.pid = pe.th32ProcessID;
            pi.ppid = pe.th32ParentProcessID;
            pi.exe = pe.szExeFile;
            v.push_back(pi);
        } while (Process32NextW(snap, &pe));
    }
    CloseHandle(snap);
    return v;
}

static bool DescendsFrom(const std::vector<ProcInfo>& ps, DWORD pid,
                         DWORD root) {
    DWORD cur = pid;
    for (int depth = 0; depth < 12 && cur && cur != root; ++depth) {
        DWORD next = 0;
        for (size_t i = 0; i < ps.size(); ++i) {
            if (ps[i].pid == cur) {
                next = ps[i].ppid;
                break;
            }
        }
        cur = next;
    }
    return cur == root;
}

// 返回本次渲染进程数（cmdline 含 --type=renderer 且祖先链含 ownerPid）
static int ReportRenderers(DWORD ownerPid, const char* tag) {
    std::vector<ProcInfo> ps = SnapshotProcs();
    int n = 0;
    for (size_t i = 0; i < ps.size(); ++i) {
        if (_wcsicmp(ps[i].exe.c_str(), L"WeChatAppEx.exe") != 0) continue;
        if (!DescendsFrom(ps, ps[i].pid, ownerPid)) continue;
        std::wstring cmd = ProcCmdline(ps[i].pid);
        if (cmd.empty()) continue;
        bool isRender = cmd.find(L"--type=renderer") != std::wstring::npos;
        bool isUtil = cmd.find(L"--type=utility") != std::wstring::npos;
        bool isGpu = cmd.find(L"--type=gpu-process") != std::wstring::npos;
        const wchar_t* role = isRender ? L"renderer"
                             : isUtil ? L"utility"
                             : isGpu  ? L"gpu"
                                      : L"browser";
        if (isRender) ++n;
        printf("[%s] pid=%lu role=%ls %ls\n", tag, ps[i].pid, role,
               cmd.substr(0, 220).c_str());
        fflush(stdout);
    }
    return n;
}

// 引擎数据目录里 users\<hash> 的存在与否，是登录态是否落盘的可观测信号
static void ReportUserDirs(const char* tag) {
    std::string p = g_rootDir + "\\users";
    printf("[%s] users dir: %s ->", tag, p.c_str());
    fflush(stdout);
    WIN32_FIND_DATAA fd;
    std::string pat = p + "\\*";
    HANDLE h = FindFirstFileA(pat.c_str(), &fd);
    if (h == INVALID_HANDLE_VALUE) {
        printf(" <missing>\n");
        return;
    }
    int n = 0;
    do {
        if (!(fd.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)) continue;
        if (strcmp(fd.cFileName, ".") == 0 || strcmp(fd.cFileName, "..") == 0) {
            continue;
        }
        printf(" %s", fd.cFileName);
        ++n;
    } while (FindNextFileA(h, &fd) && n < 8);
    FindClose(h);
    printf(" (n=%d)\n", n);
    fflush(stdout);
}

static long long OnAppletCb(void* self, void* a1, void* a2, void* a3) {
    printf("     [cb] self=%p a1=%p a2=%p a3=%p\n", self, a1, a2, a3);
    fflush(stdout);
    return 0;
}

// ---------------- 主流程 ----------------
int main() {
    // Defensive: this binary is linked as a GUI subsystem app, so no console is
    // created. If an older CONSOLE build is launched, hide its window instead.
    if (HWND console = GetConsoleWindow()) ShowWindow(console, SW_HIDE);
    SetUnhandledExceptionFilter(OnCrash);
    AddVectoredExceptionHandler(1, OnVeh);
    if (const char* appid = getenv("WMPF_APPID")) {
        g_appId = appid;
        if (g_appId.size() != 18 || g_appId.substr(0, 2) != "wx" ||
            g_appId.find_first_not_of("0123456789abcdef", 2) != std::string::npos) return 2;
        kAppId = g_appId.c_str();
    }
    if (const char* eventName = getenv("WMPF_LOGIN_EVENT")) {
        g_loginReady = OpenEventA(SYNCHRONIZE, FALSE, eventName);
        if (!g_loginReady) return 3;
    }
    // ---- 对照实验参数（环境变量驱动，默认保持基线行为）----
    // Paths are relative to this bundled workspace when provided by the launcher.
    if (const char* v = getenv("WMPF_RUNTIME_DIR")) {
        if (*v) g_runtimeA = v;
    }
    if (g_runtimeA.empty()) {
        char exe[MAX_PATH] = {};
        GetModuleFileNameA(nullptr, exe, MAX_PATH);
        std::string folder(exe);
        folder = folder.substr(0, folder.find_last_of("\\/"));
        folder = folder.substr(0, folder.find_last_of("\\/"));
        g_runtimeA = folder + "\\runtime\\5.10.7300.414";
    }
    int needed = MultiByteToWideChar(CP_ACP, 0, g_runtimeA.c_str(), -1, nullptr, 0);
    if (!needed) return 4;
    std::vector<wchar_t> runtimePath(needed);
    MultiByteToWideChar(CP_ACP, 0, g_runtimeA.c_str(), -1, runtimePath.data(), needed);
    g_runtimeW.assign(runtimePath.data());
    if (const char* v = getenv("WMPF_DATA_DIR")) { if (*v) g_dataDir = v; }
    if (const char* v = getenv("WMPF_ROOT")) {
        if (*v) g_rootDir = v;
    }
    if (const char* v = getenv("WMPF_LOGIN_BUF")) {
        if (*v) g_loginBufFile = v;
    }
    if (const char* v = getenv("WMPF_LAUNCH_CFG")) {
        if (*v) g_launchCfgFile = v;
    }
    g_obsSec = ObservationSeconds(getenv("WMPF_OBS"));
    g_noPatch = getenv("WMPF_NO_PATCH") != nullptr;
    g_useHandler = getenv("WMPF_USE_HANDLER") != nullptr;
    g_useCb = getenv("WMPF_USE_CB") != nullptr;
    g_obsKeep = getenv("WMPF_OBS_KEEP") != nullptr;

    // ★ E8 实测：官方拉起 WeChatAppEx 时传入的 env 是干净的（只有 "=C:=C:\"），
    //   而宿主传 NULL → 子进程继承我们全部 WMPF_* 变量。BROWSER 里跑的是同一个
    //   host DLL，这些同名变量会被二次读取，覆盖子进程侧的配置/登录态参数。
    //   对策：把两处"晚读"先读进局部变量，然后在调 IBC 前清空 WMPF_*（见下）。
    int e8_loginDelay = 9000;
    if (const char* v = getenv("WMPF_LOGIN_DELAY")) {
        int d = atoi(v);
        if (d >= 0) e8_loginDelay = d;
    }
    int e8_gerr = 0;
    if (const char* v = getenv("WMPF_GERR")) e8_gerr = atoi(v);
    bool e8_forceGerr = getenv("WMPF_FORCE_GERR") != nullptr;
    printf("[cfg] root=%s noPatch=%d useHandler=%d useCb=%d obs=%llds "
           "loginBuf=%s cfg=%s\n",
           g_rootDir.c_str(), (int)g_noPatch, (int)g_useHandler, (int)g_useCb,
           static_cast<long long>(g_obsSec), g_loginBufFile.c_str(), g_launchCfgFile.c_str());
    ReportUserDirs("pre");
    fflush(stdout);
    SetDllDirectoryW(g_runtimeW.c_str());
    SetCurrentDirectoryW(g_runtimeW.c_str());
    // ★ E13：DLL 初始化期可能按 cwd/相对路径找配置（官方 cwd = Application 目录，
    //   我们一直把它设成数据目录）。WMPF_CWD 可覆盖，用于验证 g_err 是否随之变化。
    if (const char* cwdov = getenv("WMPF_CWD")) {
        if (cwdov[0]) {
            std::wstring w(cwdov, cwdov + strlen(cwdov));
            if (SetCurrentDirectoryW(w.c_str()))
                printf("[cfg] cwd override -> %s\n", cwdov);
            else
                printf("[cfg] cwd override FAILED -> %s\n", cwdov);
            fflush(stdout);
        }
    }
    std::wstring hostDll = g_runtimeW + L"\\host\\wmpf_host_export_x64.dll";
    HMODULE mod = LoadLibraryW(hostDll.c_str());
    g_modBase = (uint8_t*)mod;
    printf("[load] base=%p\n", (void*)mod);
    if (!mod) return 1;
    if (!EnsureThreadEnv(mod)) return 2;

    // patch: BrowsingService 构造时 mov byte [rsi+0x92],0 -> 1
    // （applet v3 开关；官方宿主里该位为 1，否则 AppletManagerV3 不初始化）
    if (!g_noPatch) {
        uint8_t* p = (uint8_t*)mod + 0x15be42;
        DWORD old = 0;
        if (VirtualProtect(p, 1, PAGE_EXECUTE_READWRITE, &old)) {
            printf("[patch] flag92 imm: %02x -> 01\n", *p);
            *p = 1;
            VirtualProtect(p, 1, old, &old);
        }
    } else {
        printf("[patch] flag92 imm: skipped (WMPF_NO_PATCH)\n");
    }

    // patch: InitializeInternal 里 "cmp [obj+0x92],1 / jne skip" 的 jne 直接 NOP，
    // 强制调用 AppletManagerV3::Initialize（0x1dc210）
    if (!g_noPatch) {
        uint8_t* p = (uint8_t*)mod + 0x162c87;
        DWORD old = 0;
        if (VirtualProtect(p, 2, PAGE_EXECUTE_READWRITE, &old)) {
            printf("[patch] jne@0x162c87: %02x %02x -> nop\n", p[0], p[1]);
            for (int i = 0; i < 2; ++i) p[i] = 0x90;
            VirtualProtect(p, 2, old, &old);
        }
    } else {
        printf("[patch] jne@0x162c87: skipped (WMPF_NO_PATCH)\n");
    }

    g_extraCmd = L" --wmpf_root_dir=\"";
    {
        wchar_t wbuf[MAX_PATH * 2];
        MultiByteToWideChar(CP_UTF8, 0, g_rootDir.c_str(), -1, wbuf,
                            MAX_PATH * 2);
        g_extraCmd += wbuf;
    }
    g_extraCmd += L"\" --enable-applet-v3";
    // CDP：给 BROWSER 加 DevTools 端口（子进程会忽略该开关）
    if (const char* cdpPort = getenv("WMPF_CDP")) {
        int port = atoi(cdpPort);
        if (port > 0) {
            g_cdpPort = port;      // ★ E57：供 DetourCPW 单独补
            wchar_t buf[128];
            swprintf(buf, 128,
                     L" --remote-debugging-port=%d --remote-allow-origins=*",
                     port);
            g_extraCmd += buf;
            if (getenv("WMPF_CDP_INSPECTOR")) {
                // 试探：DLL 里有 enable-chrome-inspector / enable-ui-devtools 串
                g_extraCmd += L" --enable-chrome-inspector"
                              L" --enable-ui-devtools --auto-launch-devtools";
            }
            printf("[cdp] remote-debugging-port=%d inspector=%d\n", port,
                   getenv("WMPF_CDP_INSPECTOR") ? 1 : 0);
        }
    }
    HookIatCreateProcessW(mod);
    HookIatCreateNamedPipeW(mod);

    auto get_bs = (GetBrowsingService_t)GetProcAddress(mod, "GetBrowsingService");
    void* obj = get_bs();
    printf("[svc] obj=%p\n", obj);
    if (!obj) return 3;
    void** vt = *(void***)obj;

    // 1) InitilizeBrowsingCore(this, unused, runtime_root, handler)
    // ★ E21 根因：JSON 里的路径必须转义反斜杠。官方 cfg 实测为
    //   "wmpf_root_dir":"<yyb-data>\\\\wmpf_data"（双反斜杠）；
    //   我们原来把 <本机路径> 直接拼进去，`\w` 是非法 JSON 转义 → 整份配置
    //   被解析器丢弃 → g_err 停在 1000 → applet 环境不完整 → 启动即被回收。
    std::string escRoot;
    for (char c : g_rootDir) {
        if (c == '\\' || c == '"') escRoot += '\\';
        escRoot += c;
    }
    // ★ E53 发现：host DLL 内有一张 cfg 键名表（0x2FABCA 附近）：
    //   enable-ui-devtools / enable-chrome-inspector / auto-launch-devtools /
    //   xweb-enable-inspect / wait-for-debugger / use-debug-wxpkg / no-codecache /
    //   client_version / uin / wmpf_extra_config / ilink-debug-net ...
    //   与 enable-applet-v3/product-id/wmpf_root_dir 同一张表 -> 都是 cfg JSON 键。
    //   原生下发调试开关与版本/uin，替代运行时内存注入。
    // WMPF_NO_EXTRA_CFG=1：完全对齐官方 cfg（官方 BROWSER cmdline 没有
    // wmpf_extra_config / enable-*-devtools），devtools 全靠 hook patch。
    // 用于验证 scene=1000 vs 官方 1168 是否由这些开关引起。
    const bool noExtra = getenv("WMPF_NO_EXTRA_CFG") != nullptr;
    // 官方 LaunchAppletV4 参数里有 "game_type":2（小游戏模式）；宿主缺这一步，
    // 试着从 cfg JSON / wmpf_extra_config 下发，让 BROWSER 侧 scene 变 1168。
    const char* gt = getenv("WMPF_GAME_TYPE");
    const std::string gtTop = gt ? (std::string("\"game_type\":") + gt + ",")
                                 : std::string("");
    const std::string gtExtra = gt ? (std::string("\"game_type\":") + gt + ",")
                                   : std::string("");
    std::string uinJson;
    if (const char* uin = getenv("WMPF_UIN")) {
        std::string digits;
        for (const char* p = uin; *p; ++p) {
            if (*p >= '0' && *p <= '9') digits += *p;
        }
        if (!digits.empty()) uinJson = "\"uin\":\"" + digits + "\",";
    }
    std::string cfg = std::string("{\"enable-applet-v3\":true,\"log-level\":2,") +
                      gtTop +
                      "\"product-id\":3000," +
                      (noExtra ? std::string("") : std::string(
                       "\"enable-chrome-inspector\":true,"
                       "\"enable-ui-devtools\":true,"
                       "\"auto-launch-devtools\":true,"
                       "\"xweb-enable-inspect\":true,")) +
                      "\"client_version\":1661404927," +
                      uinJson +
                      (noExtra ? std::string("") : std::string(
                       "\"wmpf_extra_config\":{" + gtExtra +
                       "\"enable-chrome-inspector\":true,"
                       "\"enable-ui-devtools\":true,"
                       "\"auto-launch-devtools\":true,"
                       "\"xweb-enable-inspect\":true},")) +
                      "\"wmpf_root_dir\":\"" + escRoot + "\"}";
    printf("[cfg] json=%s\n", cfg.c_str());
    fflush(stdout);
    std::string root(g_runtimeA);
    // ★ E18 实测：官方 IBC 的 arg3 是 **runtime 目录**
    //   （<yyb>\WmpfRuntime\5.10.7300.414），而我们一直传数据目录。
    //   这里允许用 WMPF_IBC_ROOT 覆盖，用于对齐官方。
    if (const char* ibcroot = getenv("WMPF_IBC_ROOT")) {
        if (ibcroot[0]) {
            root = ibcroot;
            printf("[ibc] root 覆盖 -> %s\n", ibcroot);
            fflush(stdout);
        }
    }
    // 功能开关：AppletManagerV3::Initialize 只在 [obj+0x92]==1 时调用
    uint8_t* flags = (uint8_t*)obj;
    printf("[flags] before ibc: +0x91=%u +0x92=%u\n", flags[0x91],
           flags[0x92]);
    if (!g_noPatch) {
        if (flags[0x92] != 1) {
            flags[0x92] = 1;
            printf("[flags] force +0x92=1\n");
        }
        // 注意：官方实测 +0x91=0，宿主**不再**强写 +0x91（曾强写 1，属自造差异）
    } else {
        printf("[flags] keep as-is (WMPF_NO_PATCH)\n");
    }
    fflush(stdout);
    // ★ E27 决定性发现：宿主调用 vt[5] 时，序列采集器从未命中 0x1635C0（官方 IBC）。
    //   官方 IBC 是 exe 直接 call 0x1635C0（栈 exe+0x59a65f），**不是 vt[5]**。
    //   vt[5] 只走到 cfg 解析(0x1634B0)那一小段，缺少 IBC 主体：
    //   UP1(0x1B98C0) / UP2(0x181D90) / BR_CREATE(0x16AA70) / APPLET_INIT(0x1DC210)
    //   -> AppletManager 未初始化 -> applet 环境不完整 -> 67~165ms 秒关。
    //   这里改为直接调用 0x1635C0；WMPF_IBC_VT=1 可回退到 vt[5] 做对照。
    uint64_t (*ibc)(void*, const void*, const void*, void*);
    if (getenv("WMPF_IBC_VT")) {
        ibc = (uint64_t(*)(void*, const void*, const void*, void*))vt[5];
        printf("[ibc] 使用 vt[5]=%p（对照）\n", (void*)vt[5]);
    } else {
        ibc = (uint64_t(*)(void*, const void*, const void*, void*))
              ((uint8_t*)mod + 0x1635C0);
        printf("[ibc] 使用 IBC=mod+0x1635C0=%p（vt[5]=%p）\n",
               (void*)ibc, (void*)vt[5]);
    }
    // ★ cfg/root 必须按"引擎自己的 std::string 对象"传，不能传裸 char*。
    // 依据（wmpf_host_export_x64.dll 0x15EBB9，cfg 解析处）：
    //   movsx rax, byte ptr [rdi+0x17]   ; 长短标志
    //   jns  -> SSO 短串：data = 对象自身
    //   否则 -> 长串：data = [+0], size = [+8]
    // 传 char* 时 [+0x17] 落在文本内容上（符号位几乎必为 0）→ 被当短串，
    // 读出垃圾长度 → 整份 config 不生效（g_err 一直是 1000 的根因）。
    static char g_cfgObj[0x40];
    static char g_rootObj[0x40];
    auto MakeEngineStr = [](const std::string& s, char (&o)[0x40]) -> void* {
        memset(o, 0, sizeof(o));
        *(const char**)(o + 0x00) = s.data();
        *(uint64_t*)(o + 0x08) = (uint64_t)s.size();
        *(uint64_t*)(o + 0x10) = (uint64_t)s.size();
        o[0x17] = (char)0x80;              // 长串标志（bit7=1）
        return (void*)o;
    };
    // WMPF_IBC_HANDLER=1：给 IBC 传一个"官方同款"桩对象（官方 6 槽全是
    // 返回 0/void/false 的桩函数，见 PLAN §16.4），而不是 NULL。
    void* ibc_handler = nullptr;
    void* ibc_h = MakeHandler();
    if (getenv("WMPF_IBC_HANDLER")) ibc_handler = ibc_h;
    // 实测（tools/probe_cmdline.py）：
    //   char* 形态 → 正常拉起 BROWSER，命令行含 --wmpf_root_dir/--enable-applet-v3；
    //   对象形态 → IBC ret=0 但**不再建 BROWSER**（链路反而坏掉）。
    // 因此默认回到 char*；对象形态只保留为 WMPF_IBC_STROBJ=1 的实验开关。
    const char* stropt = getenv("WMPF_IBC_STROBJ");
    bool ibcStrObj = (stropt && stropt[0] == '1');
    void* cfgArg = ibcStrObj ? MakeEngineStr(cfg, g_cfgObj)
                             : (void*)cfg.c_str();
    void* rootArg = ibcStrObj ? MakeEngineStr(root, g_rootObj)
                              : (void*)root.c_str();
    printf("[ibc] strObj=%d cfgLen=%zu rootLen=%zu\n", (int)ibcStrObj,
           cfg.size(), root.size());
    // 调试用：调 IBC 前等待，给外部 attach/hook 留窗口
    if (const char* dl = getenv("WMPF_IBC_DELAY")) {
        int ms = atoi(dl);
        if (ms > 0) {
            printf("[ibc] delay %d ms (cfgArg=%p rootArg=%p)\n", ms, cfgArg,
                   rootArg);
            fflush(stdout);
            Sleep((DWORD)ms);
        }
    }
    const char* sw = getenv("WMPF_IBC_SWAP");
    if (sw && sw[0] == '1') {
        printf("[ibc] SWAP: (obj, root, cfg)\n");
        void* t = cfgArg;
        cfgArg = rootArg;
        rootArg = t;
    }
    // ★ E8：在触发 DLL 初始化/拉起 WeChatAppEx 之前清空 WMPF_*，
    //   让子进程环境与官方一致（官方传的就是几乎空的环境块）。
    if (!getenv("WMPF_KEEP_ENV")) {
        static const char* kEnvKeys[] = {
            "WMPF_ROOT", "WMPF_LOGIN_BUF", "WMPF_LAUNCH_CFG", "WMPF_OBS",
            "WMPF_OBS_KEEP",
            "WMPF_NO_PATCH", "WMPF_USE_HANDLER", "WMPF_USE_CB", "WMPF_CDP",
            "WMPF_CDP_INSPECTOR", "WMPF_IBC_HANDLER", "WMPF_IBC_STROBJ",
            "WMPF_IBC_DELAY", "WMPF_IBC_SWAP", "WMPF_USE_HANDLER_OBJ",
            "WMPF_LOGIN_DELAY", "WMPF_GERR", "WMPF_FORCE_GERR", "WMPF_APPID", "WMPF_LOGIN_EVENT", nullptr};
        for (int i = 0; kEnvKeys[i]; ++i)
            SetEnvironmentVariableA(kEnvKeys[i], nullptr);
        printf("[env] WMPF_* 已清空（子进程不再继承）\n");
    }
    fflush(stdout);
    uint64_t r = ibc(obj, cfgArg, rootArg, ibc_handler);
    // ★ E15：直接调用 cfg 解析函数 0x15EB30（SetErr 的唯一调用者）。
    //   依据：g_err(0x36AAB0) 静态初值=1000；只有 SetErr 能写；SetErr 的唯一
    //   引用在 0x15EB30 内（ripref 实扫）。官方 g_err=3000 说明它被调用过。
    //   第二参数按引擎布局（data@0 / size@8 / flag@0x17）传 JSON。
    if (getenv("WMPF_CFGPARSE")) {
        static char g_cfgStr[0x40];
        memset(g_cfgStr, 0, sizeof(g_cfgStr));
        *(const char**)(g_cfgStr + 0) = cfg.data();
        *(uint64_t*)(g_cfgStr + 0x8) = (uint64_t)cfg.size();
        *(uint64_t*)(g_cfgStr + 0x10) = (uint64_t)cfg.size();
        g_cfgStr[0x17] = (char)0x80;      // 长串标志
        // E16 实测：官方链路是 IBC(0x1635C0) -> 0x1634B0(264B) -> 0x15EB30 -> SetErr
        // 其中 0x1634B0 才是 cfg 解析入口，第二个参数必须是 long string 对象。
        auto cfgparse = (void(*)(void*, void*))((uint8_t*)mod + 0x1634B0);
        printf("[cfgparse] call 0x1634B0(obj=%p, cfg=%zuB) ...\n",
               (void*)obj, cfg.size());
        fflush(stdout);
        cfgparse(obj, g_cfgStr);
        printf("[cfgparse] done, g_err=%d\n",
               *(int*)((uint8_t*)mod + 0x36aab0));
        fflush(stdout);
    }
    printf("[ibc] ret=0x%llx handler=%p\n", (unsigned long long)r,
           ibc_handler);
    printf("[ibc] ret=0x%llx\n", (unsigned long long)r);
    Pump(3000);
    printf("[flags] after ibc: +0x91=%u +0x92=%u\n", flags[0x91], flags[0x92]);
    void* proxy = *(void**)((uint8_t*)obj + 0x38);
    printf("[proxy] obj+0x38=%p\n", proxy);
    if (proxy) {
        uint8_t* pf = (uint8_t*)proxy;
        printf("[proxy] flags +0x91=%u +0x92=%u  +0x28=%p +0x10=%p\n",
               pf[0x91], pf[0x92], *(void**)(pf + 0x28),
               *(void**)(pf + 0x10));
    }

    // 2) QI
    Fn3 qi = (Fn3)vt[0];
    void* mgr = nullptr;
    void* auth = nullptr;
    void* play = nullptr;
    uint64_t rc1 = qi(obj, (void*)"IAppletManagerV3", &mgr);
    uint64_t rc2 = qi(obj, (void*)"IILinkAuthManager", &auth);
    uint64_t rc3 = qi(obj, (void*)"IWePlayableApi", &play);
    printf("[qi] mgr=%p(rc=%llu) auth=%p(rc=%llu) play=%p(rc=%llu)\n", mgr,
           (unsigned long long)rc1, auth, (unsigned long long)rc2, play,
           (unsigned long long)rc3);
    if (!mgr || !auth || !play) {
        printf("[qi] 接口未取全，退出\n");
        return 4;
    }
    void** mvt = *(void***)mgr;
    void** avt = *(void***)auth;
    void** wvt = *(void***)play;
    printf("[login] auth vtable: base=%p slot3=%p slot4=%p slot5=%p\n",
           (void*)mod, avt[3], avt[4], avt[5]);
    fflush(stdout);

    // 3) 等 task runner 就绪（mgr+0x10 / auth+0x10）
    for (int i = 0; i < 40; ++i) {
        if (ReadU64(mgr, 0x10) && ReadU64(auth, 0x10)) break;
        Pump(500);
    }
    printf("[run] mgr.runner=%p auth.runner=%p\n", (void*)ReadU64(mgr, 0x10),
           (void*)ReadU64(auth, 0x10));

    // 4) 事件 handler：官方在 IBC 之后立刻调 mgr[6] 再 mgr[5]，入参是**函数指针**
    //    反汇编证据：mgr[6]=0x1DED80 做 mov [rcx+0x50],rdx；mgr[5]=0x1DECD0 做 mov [rcx+0x48],rdx
    //    官方传的是 AndrowsStore+0x171E30 / +0x172850（exe 内函数），落盘字段即 mgr+0x50/+0x48。
    void* evt_h = MakeHandler();
    void* js_h = MakeHandler();
    bool useObj = getenv("WMPF_USE_HANDLER_OBJ") != nullptr;
    void* evt_pass = useObj ? evt_h : (void*)&OnAppletEventFn;
    void* js_pass = useObj ? js_h : (void*)&OnAppletEventFn;
    uint64_t s1 = ((Fn2)mvt[6])(mgr, evt_pass);  // -> mgr+0x50
    uint64_t s2 = ((Fn2)mvt[5])(mgr, js_pass);   // -> mgr+0x48
    printf("[set] mgr[6]=%llu mgr[5]=%llu arg=%p(mode=%s) mgr50=%p mgr48=%p\n",
           (unsigned long long)s1, (unsigned long long)s2, evt_pass,
           useObj ? "object" : "fn", (void*)ReadU64(mgr, 0x50),
           (void*)ReadU64(mgr, 0x48));

    // 5) AuthSetter(auth, fn, ctx1, ctx2) —— 官方 a1=AndrowsStore+0x1726a0
    void* cbset = g_useCb ? (void*)&OnAuthSetCb : nullptr;
    if (cbset) {
        uint64_t s3 = ((Fn4)avt[7])(auth, cbset, obj, obj);
        printf("[auth] setter=%llu\n", (unsigned long long)s3);
    }

    // 6) InitApi(play, cfg, len, 1) —— 官方 a2=0x2d(45) a3=1
    std::string initcfg = ReadText("yyb_init_api_config.bin");
    uint64_t s4 = ((Fn5)wvt[0])(play, (void*)initcfg.data(),
                                (void*)initcfg.size(), (void*)1, nullptr);
    printf("[play] InitApi=%llu (len=%zu)\n", (unsigned long long)s4,
           initcfg.size());
    // AuthSetter(play, fn, 0x7ffffffffffffffc, ctx) —— 官方第二处在 play vtable[4]
    // 注意：0x1e1120 同时挂在 auth[7] 与 play[4]，play[7] 是另一个函数（调用会崩）
    if (cbset) {
        uint64_t s5 = ((Fn4)wvt[4])(play, cbset,
                                    (void*)0x7ffffffffffffffcULL, obj);
        printf("[play] setter=%llu\n", (unsigned long long)s5);
    }
    fflush(stdout);
    Pump(15000);

    // 7) 登录：官方 GetUserInfo → 9s → ThirdAppLogin → GetUserInfo
    // ★ E32 实证：官方 GetUserInfo 的第二参数是**对象**（vtable[0] 指向
    //   wmpf_helper::GetUserInfo::<lambda_1>::operator()），DLL 通过
    //   `mov rax,[obj]; call [rax]` 调它取用户信息。
    //   宿主一直传函数指针 → DLL 把代码当 vtable 读 → 用户信息为空 → user_id=0
    //   → users\md5("0") → xweb 不加载用户级 storage → JS 引擎不执行。
    // ★ 纠正（E33 实测崩溃点 addr=0x...A1E0 即 g_authVt 本身）：
    //   DLL 是 `call cb`（函数指针），不是 `call [cb]`（对象）。
    //   E32 里 a1=0x7ff616bcabc0 经 modoff 就是 exe+0x59ABC0 本身，它确实是函数地址。
    void* user_info_cb = (void*)&OnGetUserInfoTyped;
    void* login_cb = (void*)&OnLoginTyped;
    void* auth_ctx = (void*)&g_authState;
    ResetAuthState();
    printf("[login] typed callbacks: get_user_info=%p login=%p ctx=%p\n",
           user_info_cb, login_cb, auth_ctx);
    fflush(stdout);
    int* g_err = (int*)((uint8_t*)mod + 0x36aab0);
    int loginDelay = e8_loginDelay;
    int p0 = g_pipeHits;
    printf("[login] GetUserInfo(pre)=%llu g_err=%d cb=%p\n",
           (unsigned long long)((Fn3)avt[3])(auth, user_info_cb, auth_ctx),
           *g_err, user_info_cb);
    Pump(loginDelay);
    printf("[login] GetUserInfo(observed=%d status=%d type=%u uin=%llu scene=%d)\n",
           (int)InterlockedCompareExchange(&g_authState.gotUserInfo, 0, 0),
           (int)g_authState.status, (unsigned)g_authState.type,
           (unsigned long long)g_authState.uin, (int)g_authState.scene);
    printf("[login] pipe during GetUserInfo wait(%dms): %d -> %d\n", loginDelay,
           p0, g_pipeHits);
    fflush(stdout);
    std::string live = ReadText(g_loginBufFile);
    printf("[login] buffer file=%s len=%zu\n", g_loginBufFile.c_str(),
           live.size());
    const char* flow = getenv("WMPF_AUTH_FLOW");
    const bool officialFlow = !flow || strcmp(flow, "legacy") != 0;
    bool loginSkipped = false;
    if (officialFlow && g_authState.status == 0) {
        loginSkipped = true;
        printf("[login] skip login: GetUserInfo status=0\n");
        fflush(stdout);
    } else if (officialFlow && g_authState.status == 2) {
        InterlockedExchange(&g_authState.gotLogin, 0);
        printf("[login] calling AutoLogin slot=5 auth=%p cb=%p ctx=%p\n",
               auth, login_cb, auth_ctx);
        fflush(stdout);
        uint64_t autoRet = ((Fn4)avt[5])(auth, (void*)"", login_cb, auth_ctx);
        printf("[login] AutoLogin=%llu g_err=%d\n",
               (unsigned long long)autoRet, *g_err);
        Pump(5000);
        bool autoOK =
            InterlockedCompareExchange(&g_authState.gotLogin, 0, 0) != 0 &&
            g_authState.loginError == 0;
        printf("[login] AutoLogin(observed=%d error=%d type=%d ok=%d)\n",
               (int)InterlockedCompareExchange(&g_authState.gotLogin, 0, 0),
               (int)g_authState.loginError, (int)g_authState.loginType,
               (int)autoOK);
        fflush(stdout);
        bool autoRecovered = false;
        for (int attempt = 0; attempt < 8 && !autoRecovered; ++attempt) {
            InterlockedExchange(&g_authState.gotUserInfo, 0);
            ((Fn3)avt[3])(auth, user_info_cb, auth_ctx);
            Pump(2000);
            autoRecovered =
                InterlockedCompareExchange(&g_authState.gotUserInfo, 0, 0) != 0 &&
                g_authState.status == 0;
            printf("[login] GetUserInfo(after AutoLogin attempt=%d observed=%d status=%d recovered=%d)\n",
                   attempt + 1,
                   (int)InterlockedCompareExchange(&g_authState.gotUserInfo, 0, 0),
                   (int)g_authState.status, (int)autoRecovered);
            fflush(stdout);
        }
        if (autoOK && autoRecovered) {
            loginSkipped = true;
        } else if (autoOK) {
            printf("[login] AutoLogin callback succeeded but runtime login state is not ready; falling back to manual login\n");
            fflush(stdout);
        }
    }
    if (!loginSkipped) {
        if (officialFlow && (g_authState.status == 1 || g_authState.status == 2) && live.empty()) {
            printf("[login] auth_state=manual_login_required requesting login buffer\n");
            fflush(stdout);
            DWORD deadline = GetTickCount() + 120000;
            while (ReadText(g_loginBufFile).empty() &&
                   GetTickCount() < deadline) Pump(100);
            live = ReadText(g_loginBufFile);
            printf("[login] manual buffer ready len=%zu\n", live.size());
            fflush(stdout);
        }
        if (live.empty()) {
            printf("[login] no login buffer available\n");
            fflush(stdout);
            return 4;
        }
        InterlockedExchange(&g_authState.gotLogin, 0);
        uint64_t t1 = ((Fn4)avt[4])(auth, (void*)live.data(), login_cb, auth_ctx);
        printf("[login] ThirdAppLogin=%llu g_err=%d\n", (unsigned long long)t1,
               *g_err);
        fflush(stdout);
        if (g_loginReady) {
            DWORD deadline = GetTickCount() + 45000;
            while (WaitForSingleObject(g_loginReady, 0) != WAIT_OBJECT_0 &&
                   (LONG)(deadline - GetTickCount()) > 0) Pump(100);
            bool ready = WaitForSingleObject(g_loginReady, 0) == WAIT_OBJECT_0;
            CloseHandle(g_loginReady);
            g_loginReady = nullptr;
            printf("[login] coordinated_success=%d\n", (int)ready);
            fflush(stdout);
            if (!ready) return 4;
        } else Pump(5000);
        printf("[login] ThirdAppLogin(observed=%d error=%d type=%d)\n",
               (int)InterlockedCompareExchange(&g_authState.gotLogin, 0, 0),
               (int)g_authState.loginError, (int)g_authState.loginType);
    } else if (g_loginReady) {
        CloseHandle(g_loginReady);
        g_loginReady = nullptr;
    }
    InterlockedExchange(&g_authState.gotUserInfo, 0);
    printf("[login] GetUserInfo(post)=%llu g_err=%d\n",
           (unsigned long long)((Fn3)avt[3])(auth, user_info_cb, auth_ctx),
           *g_err);
    printf("[login] g_err(after)=%d cbHits=%d\n", *g_err, g_cbHits);
    ReportUserDirs("post-login");
    fflush(stdout);

    // 8) 等 mgr runner
    for (int i = 0; i < 20; ++i) {
        if (ReadU64(mgr, 0x10)) break;
        Pump(500);
    }
    printf("[run] before launch mgr.runner=%p mgr+0x18=%p mgr+0x50=%p\n",
           (void*)ReadU64(mgr, 0x10), (void*)ReadU64(mgr, 0x18),
           (void*)ReadU64(mgr, 0x50));
    printf("[own] exe=%p handler=%p jsHandler=%p evtHandler=%p\n",
           (void*)GetModuleHandleW(nullptr), (void*)&g_handler,
           (void*)js_h, (void*)evt_h);
    {
        void* p50 = (void*)ReadU64(mgr, 0x50);
        MEMORY_BASIC_INFORMATION mbi;
        if (p50 && VirtualQuery(p50, &mbi, sizeof(mbi))) {
            printf("[own] mgr+0x50 page: state=0x%lx protect=0x%lx type=0x%lx "
                   "base=%p\n",
                   mbi.State, mbi.Protect, mbi.Type, mbi.BaseAddress);
        } else {
            printf("[own] mgr+0x50 not queryable\n");
        }
    }

    // 9) LaunchApplet
    // 0x1ddaf0 里的门槛：读全局错误码 0x36aab0，
    //   >=1003 且 !=1022 -> 允许；否则跳过发送。登录失败(-10)会走到跳过分支。
    printf("[gate] g_err(before)=%d\n", *g_err);
    // g_err 真相（反汇编）：SetErr(0x12CA10) 只在 BrowsingService 构造里被调用一次，
    // 取的是配置项 "product-id"（默认 1000，clamp 到 1000..3000）。
    // 门槛 GetOk(0x12CA30) = g_err>=1003 && g_err!=1022 → 宿主 1000 时 LaunchApplet 直接不发。
    // 官方 product-id=3000 → 3000。这里允许直接对齐。
    if (e8_gerr > 0) {
        *g_err = e8_gerr;
        printf("[gate] g_err(override)=%d\n", *g_err);
    } else if (e8_forceGerr) {
        *g_err = 1003;
        printf("[gate] g_err(forced)=%d\n", *g_err);
    }
    fflush(stdout);
    // ★ 时序对齐：官方 IBC/LaunchApplet 都在 Qt/Chromium 事件循环派发的任务里调用
    //   （E6/E18/E13.1 三条栈都一样）。宿主同步立即调，DLL 内部任务可能尚未跑完，
    //   用 WMPF_LAUNCH_DELAY 先泵一段时间再 LaunchApplet。
    if (const char* ld = getenv("WMPF_LAUNCH_DELAY")) {
        int ms = atoi(ld);
        if (ms > 0) {
            printf("[launch] delay %dms（先跑 DLL 内部任务）\n", ms);
            fflush(stdout);
            Pump(ms);
        }
    }
    std::string launch = ReadText(g_launchCfgFile);
    printf("[launch] cfg file=%s len=%zu appid=%s\n", g_launchCfgFile.c_str(),
           launch.size(), kAppId);
    // Runtime 初始化需要有效的顶层 HWND。生产路径保持窗口隐藏；诊断环境可显式显示。
    // 关闭窗口会设置退出标记，观察循环及外层 observer 据此清理本次自有会话。
    HWND hostHwnd = nullptr;
    {
        InterlockedExchange(&g_hostCloseRequested, 0);
        WNDCLASSW wc = {};
        wc.lpfnWndProc = WmpfHostWndProc;
        wc.hInstance = GetModuleHandleW(nullptr);
        wc.lpszClassName = L"WmpfHostWindow";
        RegisterClassW(&wc);
        hostHwnd = CreateWindowExW(0, wc.lpszClassName, L"wmpf host",
                                   WS_OVERLAPPEDWINDOW, 0, 0, 800, 600,
                                   nullptr, nullptr, wc.hInstance, nullptr);
        if (hostHwnd && getenv("WMPF_SHOW_WND")) {
            ShowWindow(hostHwnd, SW_SHOW);
        }
        printf("[launch] hwnd=%p (visible=%d)\n", (void*)hostHwnd,
               hostHwnd && getenv("WMPF_SHOW_WND") ? 1 : 0);
        fflush(stdout);
    }
    // 第 5 参数：官方传事件回调（不是 hwnd）。用 WMPF_LAUNCH_CB=hwnd 可回退对照。
    void* launchCb = (void*)&OnAppletEvent;
    if (getenv("WMPF_LAUNCH_CB_NULL")) launchCb = nullptr;
    if (getenv("WMPF_LAUNCH_CB_HWND")) launchCb = (void*)hostHwnd;
    printf("[launch] cb=%p (hwnd=%p)\n", launchCb, (void*)hostHwnd);
    fflush(stdout);
    uint64_t lr = ((Fn5)mvt[3])(mgr, (void*)kAppId, (void*)launch.data(),
                                (void*)launch.size(), launchCb);
    printf("[launch] ret=0x%llx g_err=%d\n", (unsigned long long)lr, *g_err);
    fflush(stdout);

    // 10) 观察（判据：本次宿主名下是否出现 --type=renderer 进程）
    DWORD self = GetCurrentProcessId();
    printf("[obs] loop start (%llds; 0=unlimited)\n", static_cast<long long>(g_obsSec));
    fflush(stdout);
    int best = 0;
    const ULONGLONG obsBegan = GetTickCount64();
    const char* closeFileEnv = getenv("WMPF_CLOSE_APPID_FILE");
    const std::string closeFile = closeFileEnv ? closeFileEnv : "";
    bool bridgeClosed = false;
    for (ULONGLONG i = 0; ObservationPending(obsBegan, GetTickCount64(), g_obsSec) &&
         InterlockedCompareExchange(&g_hostCloseRequested, 0, 0) == 0; ++i) {
        Pump(500);
        if (!bridgeClosed && !closeFile.empty()) {
            std::string requested = ReadText(closeFile);
            if (!requested.empty() && requested == kAppId) {
                uint64_t closeRet = ((Fn2)mvt[4])(mgr, (void*)kAppId);
                printf("[close] CloseApplet appid=%s ret=0x%llx\n", kAppId,
                       (unsigned long long)closeRet);
                fflush(stdout);
                bridgeClosed = true;
            }
        }
        int n = ReportRenderers(self, "obs");
        if (n > best) best = n;
        printf("[obs] t=%llums renderer=%d mgrRunner=%p mgr18=%p g_err=%d\n",
               static_cast<unsigned long long>(GetTickCount64() - obsBegan), n, (void*)ReadU64(mgr, 0x10),
               (void*)ReadU64(mgr, 0x18), *g_err);
        fflush(stdout);
        // 不限时会话仅由窗口关闭/外层 worker 结束，不因 renderer 出现而退出。
        if (g_obsSec > 0 && !g_obsKeep && n > 0 && i > 4) break;
    }
    printf("[result] renderer_max=%d\n", best);
    ReportUserDirs("post-obs");
    printf("[done]\n");
    return 0;
}
