//go:build windows

package dumpclient

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func queryListenerProcess(port int) (listenerProcess, error) {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return listenerProcess{}, fmt.Errorf("netstat 失败：%w", err)
	}
	pids := parseListenerPIDs(string(out), port)
	if len(pids) == 0 {
		return listenerProcess{}, fmt.Errorf("未找到监听 TCP 端口的进程")
	}
	var fallback listenerProcess
	for _, pid := range pids {
		path, err := processImagePath(pid)
		if err != nil {
			continue
		}
		info := listenerProcess{PID: pid, Path: path}
		if strings.EqualFold(filepath.Base(path), exeName) {
			return info, nil
		}
		if fallback.PID == 0 {
			fallback = info
		}
	}
	if fallback.PID != 0 {
		return fallback, nil
	}
	return listenerProcess{}, fmt.Errorf("无法读取监听进程的映像路径")
}

func processImagePath(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}
