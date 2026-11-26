package config

import "context"

// AcquireLock shares the same OS-backed locking primitive with native workers.
// The caller creates the parent directory and releases the returned lock.
func AcquireLock(ctx context.Context, path string) (func(), error) { return lockFile(ctx, path) }
