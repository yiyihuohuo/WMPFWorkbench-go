// Package embedded runs the login service inside the desktop process. The
// service keeps its own SQLite file and resource root, so the behaviour matches
// the standalone executable while avoiding a released helper binary.
package embedded

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"yyb_go/internal/httpapi"
)

type Service struct {
	server *http.Server
	app    *httpapi.App
	URL    string
}

func Start(resourceRoot string, port int, token string) (*Service, error) {
	var srv *http.Server
	cfg := httpapi.Config{
		ResourceRoot:      resourceRoot,
		DBFilename:        httpapi.DefaultDBFilename,
		SessionTTL:        30 * time.Minute,
		RequestTimeout:    45 * time.Second,
		AvatarTimeout:     10 * time.Second,
		ScanTimeout:       180 * time.Second,
		QRSessionTTL:      5 * time.Minute,
		KeepAliveInterval: 5 * time.Minute,
		DesktopToken:      token,
		Shutdown: func() {
			go func() {
				if srv == nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}()
		},
	}
	app, err := httpapi.NewApp(cfg)
	if err != nil {
		return nil, err
	}
	srv = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		_ = app.Close()
		return nil, err
	}
	go func() { _ = srv.Serve(listener) }()
	return &Service{server: srv, app: app, URL: "http://" + srv.Addr}, nil
}

func (s *Service) Stop(ctx context.Context) {
	_ = s.server.Shutdown(ctx)
	_ = s.app.Close()
}
