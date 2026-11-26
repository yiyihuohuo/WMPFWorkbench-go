// Package progress 输出结构化进度事件（本 fork 新增，非上游代码）。
//
// 设计约束：
//   - 默认关闭，仅当环境变量 KILLWXAPKG_PROGRESS=1 时输出，避免影响
//     上游原有的日志行为；
//   - 事件写 stderr，每行一个 JSON，前缀固定为 "PROGRESS "，调用方按
//     前缀过滤，其余行仍是原来的 log 文本；
//   - 解包阶段是 10 个 goroutine 并发，所有计数与输出都加锁。
package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	enabled = os.Getenv("KILLWXAPKG_PROGRESS") == "1"
	mu      sync.Mutex
	// 包级计数
	totalPackages int
	donePackages  int
)

// Enabled 是否开启进度输出
func Enabled() bool { return enabled }

// SetPackages 记录总包数（在开始处理前调用）
func SetPackages(n int) {
	mu.Lock()
	totalPackages = n
	donePackages = 0
	mu.Unlock()
	Emit("pipeline_start", map[string]interface{}{"total_packages": n})
}

// PackageDone 标记一个包处理完成
func PackageDone() {
	mu.Lock()
	donePackages++
	d := donePackages
	t := totalPackages
	mu.Unlock()
	Emit("package_done", map[string]interface{}{
		"done_packages":  d,
		"total_packages": t,
	})
}

// Snapshot 返回当前包级计数
func Snapshot() (done int, total int) {
	mu.Lock()
	defer mu.Unlock()
	return donePackages, totalPackages
}

// Emit 输出一条进度事件；未开启时直接返回
func Emit(stage string, kv map[string]interface{}) {
	if !enabled {
		return
	}
	ev := map[string]interface{}{
		"stage": stage,
		"ts":    time.Now().UnixMilli(),
		"pid":   os.Getpid(),
	}
	for k, v := range kv {
		ev[k] = v
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	mu.Lock()
	fmt.Fprintf(os.Stderr, "PROGRESS %s\n", b)
	mu.Unlock()
}
