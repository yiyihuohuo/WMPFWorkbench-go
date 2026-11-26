#pragma once
#include <windows.h>

inline volatile LONG g_hostCloseRequested = 0;

inline LRESULT CALLBACK WmpfHostWndProc(HWND window, UINT message, WPARAM wparam, LPARAM lparam) {
    if (message == WM_CLOSE) {
        InterlockedExchange(&g_hostCloseRequested, 1);
        DestroyWindow(window);
        return 0;
    }
    if (message == WM_DESTROY) {
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(window, message, wparam, lparam);
}
