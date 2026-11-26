package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yyb_go/internal/protocol"
	"yyb_go/internal/qr"
	"yyb_go/internal/store"
)

type authTransport func(*http.Request) (*http.Response, error)

func (f authTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func authReply(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func lifecycleApp(t *testing.T, transport authTransport) (*App, *store.WechatAccount) {
	t.Helper()
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), RequestTimeout: time.Second, AvatarTimeout: time.Millisecond, KeepAliveInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	client := protocol.NewLoginBufferClientWithHTTPClient(time.Second, &http.Client{Transport: transport})
	app.qr = qr.NewClientWithLoginBufferClient(time.Second, client)
	status := "alive"
	acc, err := app.db.UpsertAccount(context.Background(), "test-open", "old-ticket", nil, nil, nil, nil,
		map[string]any{"openid": "test-open", "accesstoken": "old-token", "refreshtoken": "old-refresh", "guid": "test-guid", "logintype": "WX", "scope": "snsapi_login,snsapi_runtime_pcsdk", "expires_at": time.Now().Add(time.Hour).Unix()}, &status)
	if err != nil {
		t.Fatal(err)
	}
	return app, acc
}

func TestRefreshPersistsNewCredentialsBeforeTicketFailure(t *testing.T) {
	var app *App
	var acc *store.WechatAccount
	app, acc = lifecycleApp(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "check_login") || strings.Contains(r.URL.Path, "refresh_token") {
			return authReply(`{"code":0,"user_info":{"access_token":"new-token","refresh_token":"new-refresh","expires_in":7200}}`), nil
		}
		stored, err := app.db.GetAccount(context.Background(), acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Credentials["accesstoken"] != "new-token" {
			t.Error("new credentials must be durable before ticket HTTP request")
		}
		return nil, errors.New("temporary connection error")
	})
	got := app.refreshLiveness(context.Background(), acc)
	if got == "expired" || got == "alive" {
		t.Fatalf("ticket transport error misclassified as %s", got)
	}
	stored, err := app.db.GetAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Credentials["accesstoken"] != "new-token" || stored.LoginBuffer != "old-ticket" {
		t.Fatal("partial credential success was lost or previous ticket was erased")
	}
	if stored.Status == nil || *stored.Status == "expired" {
		t.Fatal("temporary failure must not expire account")
	}
}

func TestNetworkFailureDoesNotExpireOrRequestRescan(t *testing.T) {
	app, acc := lifecycleApp(t, func(r *http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	_, err := app.invokeWXApp(context.Background(), acc, "wx1111111111111111", nil, func(context.Context, *store.WechatAccount, string, map[string]any) (map[string]any, error) {
		t.Fatal("business request must not proceed after failed renewal")
		return nil, nil
	})
	var expired accountExpiredError
	if errors.As(err, &expired) {
		t.Fatal("network outage incorrectly requests re-scan")
	}
	if err == nil {
		t.Fatal("expected network failure")
	}
}

func TestDatabaseFailureCannotReturnAlive(t *testing.T) {
	app, acc := lifecycleApp(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("must detect unavailable store before network")
		return nil, nil
	})
	app.db.Close()
	if got := app.refreshLiveness(context.Background(), acc); got == "alive" {
		t.Fatal("closed store cannot be reported alive")
	}
}

func TestConcurrentRenewalRereadsLatestRotatedCredentials(t *testing.T) {
	rotations := 0
	app, acc := lifecycleApp(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			return authReply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
		}
		var obj struct {
			UserInfo struct {
				AccessToken string `json:"accessToken"`
			} `json:"userInfo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
			return nil, err
		}
		expected := "old-token"
		if rotations > 0 {
			expected = "rotated"
		}
		if obj.UserInfo.AccessToken != expected {
			t.Error("concurrent request reused a stale credential snapshot")
		}
		rotations++
		return authReply(`{"code":0,"user_info":{"access_token":"rotated","refresh_token":"rotated-refresh","expires_in":"7200"}}`), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if app.refreshLiveness(context.Background(), acc) != "alive" {
				t.Error("renewal failed")
			}
		}()
	}
	wg.Wait()
	if rotations != 2 {
		t.Fatal("both requests must complete")
	}
}

func TestKeepAliveRenewsWithoutAPIRequestsAndStopsBeforeStoreClose(t *testing.T) {
	called := make(chan struct{}, 4)
	app, acc := lifecycleApp(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			called <- struct{}{}
			return authReply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
		}
		return authReply(`{"code":0,"user_info":{"access_token":"background-token","refresh_token":"background-refresh","expires_in":"7200"}}`), nil
	})
	app.startKeepAlive(10 * time.Millisecond)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("no background renewal without API activity")
	}
	// Serialize behind the in-flight account renewal before inspecting storage.
	lock, _ := app.refreshLocks.Load(acc.ID)
	mu := lock.(*sync.Mutex)
	mu.Lock()
	stored, err := app.db.GetAccount(context.Background(), acc.ID)
	mu.Unlock()
	if err != nil || stored.LoginBuffer != "dGlja2V0" || stored.Credentials["accesstoken"] != "background-token" {
		t.Fatal("background renewal was not persisted")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-app.keepAliveDone:
	default:
		t.Fatal("store closed while keepalive was still running")
	}
}

// Opt-in test uses an isolated database and performs no business API calls.
func TestLiveAccountKeepAlive(t *testing.T) {
	path := os.Getenv("YYB_LOGIN_CHECK_STATE")
	if path == "" {
		t.Skip("set YYB_LOGIN_CHECK_STATE to a private login checkpoint")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private checkpoint")
	}
	var input struct {
		Credentials map[string]any `json:"credentials"`
	}
	if json.Unmarshal(data, &input) != nil || input.Credentials == nil {
		t.Fatal("invalid private checkpoint")
	}
	creds := protocol.CredentialsFromMap(input.Credentials)
	app, err := NewApp(Config{ResourceRoot: t.TempDir(), RequestTimeout: 25 * time.Second, KeepAliveInterval: -1})
	if err != nil {
		t.Fatal("cannot initialize isolated login service")
	}
	defer app.Close()
	status := "alive"
	acc, err := app.db.UpsertAccount(context.Background(), creds.OpenID, "pending-live-renewal", nil, nil, nil, nil, creds.ToMap(), &status)
	if err != nil {
		t.Fatal("cannot initialize isolated account")
	}
	app.startKeepAlive(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("background renewal did not establish durable live state")
		case <-ticker.C:
			stored, err := app.db.GetAccount(ctx, acc.ID)
			if err != nil {
				t.Fatal("cannot inspect isolated account")
			}
			if stored.LoginBuffer == "pending-live-renewal" || stored.Status == nil || *stored.Status != "alive" {
				continue
			}
			app.keepAliveCancel()
			<-app.keepAliveDone
			stored, err = app.db.GetAccount(ctx, acc.ID)
			if err != nil {
				t.Fatal("cannot read final isolated account state")
			}
			// Preserve the latest server-issued credentials before ending this run.
			state, err := json.Marshal(map[string]any{"credentials": stored.Credentials, "login_buffer": stored.LoginBuffer})
			if err != nil || os.WriteFile(path, state, 0600) != nil {
				t.Fatal("cannot preserve private live state")
			}
			if err := app.Close(); err != nil {
				t.Fatal("cannot stop isolated login service")
			}
			t.Log("real background renewal succeeded without API calls; credentials and ticket persisted in isolated database")
			return
		}
	}
}

func TestKeepAliveShutdownWaitsForInflightCredentialUpdate(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var app *App
	app, _ = lifecycleApp(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "login_buffer") {
			return authReply(`{"code":0,"ext_info":{"list_s":{"login_buffer":{"value":["dGlja2V0"]}}}}`), nil
		}
		close(started)
		<-finish
		if r.Context().Err() != nil {
			t.Error("graceful shutdown canceled a pending credential rotation")
		}
		return authReply(`{"code":0,"user_info":{"access_token":"shutdown-token","refresh_token":"shutdown-refresh","expires_in":"7200"}}`), nil
	})
	app.startKeepAlive(time.Millisecond)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("keepalive request did not start")
	}
	app.keepAliveCancel()
	close(finish)
	select {
	case <-app.keepAliveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("keepalive did not stop")
	}
	accounts, err := app.db.ListAccounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].Credentials["accesstoken"] != "shutdown-token" || accounts[0].LoginBuffer != "dGlja2V0" {
		t.Fatal("graceful shutdown lost the completed credential rotation")
	}
}
