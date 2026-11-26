package qr

import (
	"context"
	"os"
	"testing"
	"time"
)

// Generates a real QR image only when explicitly requested. Scanning and
// authorization remain an interactive step; no OAuth code is synthesized.
func TestLiveQRCodeCreation(t *testing.T) {
	if os.Getenv("YYB_LOGIN_CHECK_QR") != "1" {
		t.Skip("set YYB_LOGIN_CHECK_QR=1 to verify real QR creation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := NewClient(25 * time.Second).GetQRCodeImage(ctx)
	if err != nil {
		t.Fatal("real QR creation failed; upstream details withheld")
	}
	if len(result.ImageBytes) < 100 || result.Session.ClientGuid == "" {
		t.Fatal("QR creation returned incomplete state")
	}
	t.Logf("real OAuth QR creation succeeded; image_bytes=%d; session identity withheld", len(result.ImageBytes))
}
