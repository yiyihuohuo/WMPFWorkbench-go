package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/bundle"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/core"
	"wmpfworkbench/internal/hostruntime"
	"wmpfworkbench/internal/mcpserver"
)

// runMCP serves the same core.Service in MCP mode. The stdio transport must be
// started by the MCP client (it owns the pipes), but it can live in this single
// executable because the SDK only needs stdin/stdout.
func runMCP(root, transport string, port int, cfgPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runMCPWithContext(ctx, root, transport, port, cfgPath)
}

// runMCPWithContext is the cancelable form used when this process is the MCP
// server itself (the -mcp flag). It owns its own Service because no GUI exists.
func runMCPWithContext(ctx context.Context, root, transport string, port int, cfgPath string) error {
	p, err := app.Resolve(root)
	if err != nil {
		return err
	}
	if helpers, err := bundle.Ensure(p.Data); err == nil {
		bundle.Apply(helpers)
	}
	service := core.New(p)
	defer service.Close()
	if cfgPath != "" {
		service.Config = config.New(cfgPath)
		service.Account.Config = service.Config
		service.Runtime = hostruntime.New(p, service.Config, service.Account)
	}
	return serveMCP(ctx, service, transport, port)
}

// serveMCP serves an existing Service over the requested transport. The GUI
// calls this with its own Service so the MCP endpoint and the GUI share one
// login service, one runtime session table and one config store. Creating a
// second Service here used to make the GUI and MCP fight over the login port
// (each Ensure could stop and restart the other's service mid-scan).
func serveMCP(ctx context.Context, service *core.Service, transport string, port int) error {
	server, err := mcpserver.New(service)
	if err != nil {
		return err
	}
	switch transport {
	case "stdio":
		return server.Run(ctx, &mcp.StdioTransport{})
	case "http":
		values, err := service.Config.Read()
		if err != nil {
			return err
		}
		if port == 0 {
			port = config.Int(values, "mcp_port")
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid port")
		}
		bind := config.String(values, "mcp_bind")
		if bind == "" {
			bind = "127.0.0.1"
		}
		if bind != "127.0.0.1" && bind != "0.0.0.0" {
			return fmt.Errorf("invalid mcp_bind: %s", bind)
		}
		token := config.String(values, "mcp_token")
		if token == "" {
			// Mirror loginsvc: mint and persist a token on first run so a fresh
			// default config can start the HTTP MCP endpoint instead of failing
			// with "desktop MCP access token missing".
			var raw [32]byte
			if _, randErr := rand.Read(raw[:]); randErr != nil {
				return randErr
			}
			token = base64.RawURLEncoding.EncodeToString(raw[:])
			if _, updateErr := service.Config.Update(context.Background(), map[string]any{"mcp_token": token}); updateErr != nil {
				return updateErr
			}
		}
		stop := make(chan struct{}, 1)
		handler, err := mcpserver.HTTP(service, server, token, func() {
			select {
			case stop <- struct{}{}:
			default:
			}
		})
		if err != nil {
			return err
		}
		listener, err := mcpListen(bind, port)
		if err != nil {
			return err
		}
		httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		done := make(chan error, 1)
		go func() { done <- httpServer.Serve(listener) }()
		select {
		case err := <-done:
			if err != http.ErrServerClosed {
				return err
			}
		case <-ctx.Done():
		case <-stop:
		}
		shutdownCtx, close := context.WithTimeout(context.Background(), 5*time.Second)
		defer close()
		httpServer.Shutdown(shutdownCtx)
		return nil
	}
	return fmt.Errorf("transport must be stdio or http")
}

// mcpListen is the single place that turns the configured bind address into a
// listener. Kept separate so a test can prove 127.0.0.1 vs 0.0.0.0 actually
// changes which interfaces accept connections.
func mcpListen(bind string, port int) (net.Listener, error) {
	if bind == "" {
		bind = "127.0.0.1"
	}
	if bind != "127.0.0.1" && bind != "0.0.0.0" {
		return nil, fmt.Errorf("invalid mcp_bind: %s", bind)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port")
	}
	return net.Listen("tcp", fmt.Sprintf("%s:%d", bind, port))
}
