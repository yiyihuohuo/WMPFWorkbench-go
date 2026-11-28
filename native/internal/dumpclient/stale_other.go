//go:build !windows

package dumpclient

import "fmt"

func queryListenerProcess(port int) (listenerProcess, error) {
	return listenerProcess{}, fmt.Errorf("当前平台不支持自动检测旧版 wxapkg-dump")
}

func processImagePath(pid int) (string, error) {
	return "", fmt.Errorf("当前平台不支持读取进程映像路径")
}
