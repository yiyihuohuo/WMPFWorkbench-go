package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"yyb_go/internal/httpapi"
)

func main() {
	host := flag.String("host", "127.0.0.1", "listen host")
	port := flag.Int("port", 8000, "listen port")
	resourceRoot := flag.String("resource-root", filepath.Join(".", "resource"), "runtime resource directory")
	dbFilename := flag.String("db", httpapi.DefaultDBFilename, "SQLite database filename under resource/db")
	tcpProxy := flag.String("tcp-proxy", "", "optional TCP proxy: socks5://host:port or http-connect://host:port")
	// 本地补丁：微信 qrconnect 是长轮询（实测挂起 ~19s），默认 8s 会稳定 502
	reqTimeout := flag.Duration("req-timeout", 45*time.Second, "upstream HTTP timeout (qr long-poll needs >20s)")
	keepAlive := flag.Duration("keepalive-interval", 5*time.Minute, "login check interval; negative disables background renewal")
	flag.Parse()
	stop := make(chan os.Signal, 1)
	var shutdownOnce sync.Once

	cfg := httpapi.Config{
		ResourceRoot:      *resourceRoot,
		DBFilename:        *dbFilename,
		TCPProxy:          *tcpProxy,
		SessionTTL:        30 * time.Minute,
		RequestTimeout:    *reqTimeout,
		AvatarTimeout:     10 * time.Second,
		ScanTimeout:       180 * time.Second,
		QRSessionTTL:      5 * time.Minute,
		KeepAliveInterval: *keepAlive,
		DesktopToken:      os.Getenv("YYB_DESKTOP_TOKEN"),
		Shutdown:          func() { shutdownOnce.Do(func() { stop <- os.Interrupt }) },
	}

	app, err := httpapi.NewApp(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer app.Close()

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("YYB Go service listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
