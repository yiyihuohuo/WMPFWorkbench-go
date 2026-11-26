//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32             = windows.NewLazySystemDLL("user32.dll")
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procFindWindowW    = user32.NewProc("FindWindowW")
	procSendMessageW   = user32.NewProc("SendMessageW")
	procExtractIconExW = shell32.NewProc("ExtractIconExW")
)

const (
	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
)

// applyWindowIcon copies the executable's icon group onto the created window,
// so the title bar and taskbar match the file icon. Wails does not set WM_SETICON
// on Windows, and the exe resource alone is not enough for the window class.
func applyWindowIcon(title, exe string) {
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return
	}
	var big, small uintptr
	count, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(exePtr)), 0,
		uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	if count == 0 || count == 0xffffffff {
		return
	}
	if small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
	if big != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
}
