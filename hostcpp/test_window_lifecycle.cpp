#include <windows.h>
#include <cassert>
#include "window_lifecycle.h"

int main() {
    HINSTANCE instance=GetModuleHandleW(nullptr);
    WNDCLASSW wc{};
    wc.lpfnWndProc=WmpfHostWndProc;
    wc.hInstance=instance;
    wc.lpszClassName=L"WmpfHostWindowLifecycleTest";
    assert(RegisterClassW(&wc) || GetLastError()==ERROR_CLASS_ALREADY_EXISTS);
    InterlockedExchange(&g_hostCloseRequested,0);
    HWND window=CreateWindowExW(0,wc.lpszClassName,L"test",WS_OVERLAPPEDWINDOW,
                                0,0,160,100,nullptr,nullptr,instance,nullptr);
    assert(window && IsWindow(window));
    SendMessageW(window,WM_CLOSE,0,0);
    assert(InterlockedCompareExchange(&g_hostCloseRequested,0,0)==1);
    assert(!IsWindow(window));
    return 0;
}
