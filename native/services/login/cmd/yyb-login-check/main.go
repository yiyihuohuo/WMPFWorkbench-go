// yyb-login-check validates YYB login without starting the official client.
// Credentials enter through stdin; only redacted status is written to stdout.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"yyb_go/internal/protocol"
)

func savePrivate(path string, creds protocol.LoginBufferCredentials, ticket string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create credential directory failed")
	}
	data, err := json.Marshal(map[string]any{"credentials": creds.ToMap(), "login_buffer": ticket})
	if err != nil {
		return fmt.Errorf("encode private checkpoint failed")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".login-state-*")
	if err != nil {
		return fmt.Errorf("create private checkpoint failed")
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write private checkpoint failed")
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("publish private checkpoint failed")
	}
	return nil
}
func emit(value any) { json.NewEncoder(os.Stdout).Encode(value) }
func run() int {
	mode := flag.String("mode", "ticket", "ticket, renew, or refresh (ordinary main login)")
	state := flag.String("state", "", "private checkpoint file, required for renew/refresh")
	flag.Parse()
	if *mode != "ticket" && *mode != "renew" && *mode != "refresh" {
		emit(map[string]any{"ok": false, "error": "invalid mode"})
		return 1
	}
	if *mode != "ticket" && *state == "" {
		emit(map[string]any{"ok": false, "error": "renew/refresh requires a private state file"})
		return 1
	}
	var input struct {
		Credentials map[string]any `json:"credentials"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.Credentials == nil {
		emit(map[string]any{"ok": false, "error": "expected credentials object on stdin"})
		return 1
	}
	creds := protocol.CredentialsFromMap(input.Credentials)
	client := protocol.NewLoginBufferClient(25 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	save := func(c protocol.LoginBufferCredentials) error {
		err := savePrivate(*state, c, "")
		if err == nil {
			emit(map[string]any{"event": "credentials_saved", "expires_in": c.ExpiresIn})
		}
		return err
	}
	var ticket string
	var err error
	switch *mode {
	case "renew":
		result, e := client.RefreshLoginBufferWithCheckpoint(ctx, creds, save)
		creds, ticket, err = result.Credentials, result.LoginBuffer, e
	case "refresh":
		creds, err = client.RefreshCredentials(ctx, creds)
		if err == nil {
			err = save(creds)
		}
		if err == nil {
			ticket, err = client.FetchLoginBuffer(ctx, creds)
		}
	default:
		ticket, err = client.FetchLoginBuffer(ctx, creds)
	}
	if err != nil {
		emit(map[string]any{"ok": false, "mode": *mode, "error": err.Error(), "reauthorize": protocol.RequiresReauthorization(err)})
		return 1
	}
	decoded, e := base64.StdEncoding.DecodeString(ticket)
	if e != nil || len(decoded) == 0 {
		emit(map[string]any{"ok": false, "error": "invalid login buffer encoding"})
		return 1
	}
	if *state != "" {
		if err = savePrivate(*state, creds, ticket); err != nil {
			emit(map[string]any{"ok": false, "error": err.Error()})
			return 1
		}
	}
	emit(map[string]any{"ok": true, "mode": *mode, "ticket_chars": len(ticket), "ticket_bytes": len(decoded), "expires_in": creds.ExpiresIn, "has_sdk_namespace": creds.WXApp.OpenID != ""})
	return 0
}
func main() { os.Exit(run()) }
