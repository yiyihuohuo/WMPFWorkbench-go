package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestDiagLiveBridge(t *testing.T) {
	if os.Getenv("DIAG_LIVE_BRIDGE") != "1" {
		t.Skip("set DIAG_LIVE_BRIDGE=1")
	}
	addr := os.Getenv("DIAG_BRIDGE_ADDR")
	if addr == "" {
		addr = "ws://127.0.0.1:62000"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	probe := func(label, expr string) {
		raw, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{
			"expression": expr, "returnByValue": true,
		}, "")
		if err != nil {
			fmt.Printf("%s -> ERROR %v\n", label, err)
			return
		}
		var r struct {
			Result struct {
				Value any `json:"value"`
			} `json:"result"`
			ExceptionDetails json.RawMessage `json:"exceptionDetails"`
		}
		_ = json.Unmarshal(raw, &r)
		fmt.Printf("%s -> value=%v exception=%v\n", label, r.Result.Value, len(r.ExceptionDetails) > 0)
	}
	probe("1+1", "1+1")
	probe("appid", `(function(){try{if(typeof wx!=='undefined'&&wx.getAccountInfoSync){return wx.getAccountInfoSync().miniProgram.appId}}catch(e){}try{if(typeof __wxConfig!=='undefined'){return __wxConfig.appId}}catch(e){}return ''})()`)
	probe("title", `(typeof __wxConfig!=='undefined'&&__wxConfig.appName)||document.title||''`)
}
