//go:build !windows

package dumpclient

import "fmt"

func queryListenerProcess(port int) (listenerProcess, error) {
	return listenerProcess{}, fmt.Errorf("当前平台不支持自动检测旧版 wxapkg-dump")
}
