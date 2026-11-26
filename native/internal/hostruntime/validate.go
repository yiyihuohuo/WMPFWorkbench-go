package hostruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Fixed RVAs in the embedded agents apply only to these measured binaries.
var runtimeHashes = map[string]string{
	"runtime/flue.dll":              "a66d9388ff0b6fbd79045551e9d5b22dadc460ebd1b5659733361921617e22f6",
	"host/wmpf_host_export_x64.dll": "d4ee4ebd7066bf1fca47e1b7003f21d6e240afd5b5209432938bcf86f38f6025",
	"runtime/libamssdk.dll":         "80e80064578b6cc155ffdcfd67520f5013c71d37611876fe1768dfa59f4b4807",
	"runtime/pcyyb_sdk_dll.dll":     "426bee2cd0ffb26d44e1e2d852b5545d6ac8a9c5a682db779975ad13d2f11e2d",
	"runtime/WeChatAppEx.exe":       "2a927250e538ed6194e0136fc36e92cb727d5008ac1953d7a7a41364e20c15d3",
}

const runtimeHelp = "当前缺少微信 WMPF 运行时文件，无法启动小程序。" +
	"请安装应用宝 PC 版，把其安装目录下的 WmpfRuntime\\5.10.7300.414\\ 整个目录拷贝为工作台根目录的 runtime\\5.10.7300.414\\（约 386MB，含 host\\ 与 runtime\\ 子目录）。" +
	"这些 DLL 因可能侵权不随项目分发，必须由使用者本机提供。"

// ValidateRuntime checks every required runtime file and reports ALL missing
// or mismatched files at once, with an actionable fix, instead of failing on
// the first file with an opaque message.
func ValidateRuntime(ctx context.Context, root string) error {
	var missing []string
	var mismatched []string
	for name, want := range runtimeHashes {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			missing = append(missing, name)
			continue
		}
		h := sha256.New()
		buf := make([]byte, 1<<20)
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			var n int
			n, err = f.Read(buf)
			if n > 0 {
				h.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		f.Close()
		if err != nil && err != io.EOF {
			mismatched = append(mismatched, name+"（读取失败："+err.Error()+"）")
			continue
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			mismatched = append(mismatched, fmt.Sprintf("%s（实际 %s，期望 %s）", name, got, want))
		}
	}
	if len(missing) == 0 && len(mismatched) == 0 {
		return nil
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "缺失文件: "+strings.Join(missing, ", "))
	}
	if len(mismatched) > 0 {
		parts = append(parts, "校验不匹配: "+strings.Join(mismatched, ", "))
	}
	return errors.New("运行时预检失败。" + strings.Join(parts, "；") + "。" + runtimeHelp)
}
