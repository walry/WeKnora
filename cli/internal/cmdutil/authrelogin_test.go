package cmdutil

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/cli/internal/secrets"
	sdk "github.com/Tencent/WeKnora/client"
)

// fakeLoginRefresher implements LoginRefresher with scripted responses, so the
// refresh-then-relogin decision can be exercised without HTTP.
type fakeLoginRefresher struct {
	refreshResp *sdk.RefreshTokenResponse
	refreshErr  error
	loginResp   *sdk.LoginResponse
	loginErr    error

	refreshCalls int
	loginCalls   int
	gotEmail     string
	gotPassword  string
}

func (f *fakeLoginRefresher) RefreshToken(_ context.Context, _ string) (*sdk.RefreshTokenResponse, error) {
	f.refreshCalls++
	return f.refreshResp, f.refreshErr
}

func (f *fakeLoginRefresher) Login(_ context.Context, req sdk.LoginRequest) (*sdk.LoginResponse, error) {
	f.loginCalls++
	f.gotEmail = req.Email
	f.gotPassword = req.Password
	return f.loginResp, f.loginErr
}

func errCode(t *testing.T, err error) ErrorCode {
	t.Helper()
	var typed *Error
	require.ErrorAs(t, err, &typed)
	return typed.Code
}

// Refresh succeeds: the relogin fallback must NOT run.
func TestRefreshOrRelogin_RefreshSucceeds(t *testing.T) {
	store := secrets.NewMemStore()
	require.NoError(t, store.Set("p", "refresh", "rt"))
	require.NoError(t, store.Set("p", "password", "pw"))

	svc := &fakeLoginRefresher{refreshResp: &sdk.RefreshTokenResponse{
		Success: true, AccessToken: "new-access", RefreshToken: "new-refresh",
	}}

	tok, err := RefreshOrRelogin(context.Background(), store, svc, "p", "a@b.c")
	require.NoError(t, err)
	assert.Equal(t, "new-access", tok)
	assert.Equal(t, 1, svc.refreshCalls)
	assert.Equal(t, 0, svc.loginCalls, "login must not run when refresh succeeds")
}

// Refresh is rejected but a password is remembered: relogin succeeds and
// persists the fresh token pair.
func TestRefreshOrRelogin_RejectedRefresh_ReloginSucceeds(t *testing.T) {
	store := secrets.NewMemStore()
	require.NoError(t, store.Set("p", "refresh", "rt"))
	require.NoError(t, store.Set("p", "password", "s3cret"))

	svc := &fakeLoginRefresher{
		refreshResp: &sdk.RefreshTokenResponse{Success: false, Message: "revoked"},
		loginResp:   &sdk.LoginResponse{Success: true, Token: "relogin-access", RefreshToken: "relogin-refresh"},
	}

	tok, err := RefreshOrRelogin(context.Background(), store, svc, "p", "a@b.c")
	require.NoError(t, err)
	assert.Equal(t, "relogin-access", tok)
	assert.Equal(t, 1, svc.loginCalls)
	assert.Equal(t, "a@b.c", svc.gotEmail)
	assert.Equal(t, "s3cret", svc.gotPassword)

	gotAccess, _ := store.Get("p", "access")
	gotRefresh, _ := store.Get("p", "refresh")
	assert.Equal(t, "relogin-access", gotAccess)
	assert.Equal(t, "relogin-refresh", gotRefresh)
}

// Refresh is rejected and NO password is remembered: the original
// auth.token_expired error is surfaced and login is never attempted.
func TestRefreshOrRelogin_RejectedRefresh_NoPassword(t *testing.T) {
	store := secrets.NewMemStore()
	require.NoError(t, store.Set("p", "refresh", "rt"))

	svc := &fakeLoginRefresher{refreshResp: &sdk.RefreshTokenResponse{Success: false}}

	_, err := RefreshOrRelogin(context.Background(), store, svc, "p", "a@b.c")
	require.Error(t, err)
	assert.Equal(t, CodeAuthTokenExpired, errCode(t, err))
	assert.Equal(t, 0, svc.loginCalls)
}

// Relogin is definitively rejected (e.g. the password was changed): the stale
// password is dropped and the original expired error is returned.
func TestRefreshOrRelogin_ReloginRejected_ClearsPassword(t *testing.T) {
	store := secrets.NewMemStore()
	require.NoError(t, store.Set("p", "refresh", "rt"))
	require.NoError(t, store.Set("p", "password", "stale"))

	svc := &fakeLoginRefresher{
		refreshResp: &sdk.RefreshTokenResponse{Success: false},
		loginErr:    errors.New("HTTP error 401: invalid email or password"),
	}

	_, err := RefreshOrRelogin(context.Background(), store, svc, "p", "a@b.c")
	require.Error(t, err)
	assert.Equal(t, CodeAuthTokenExpired, errCode(t, err))
	assert.Equal(t, 1, svc.loginCalls)

	_, perr := store.Get("p", "password")
	assert.ErrorIs(t, perr, secrets.ErrNotFound, "stale password must be cleared")
}

// A transient network failure during refresh is NOT recoverable via relogin
// and must propagate unchanged (never silently re-login).
func TestRefreshOrRelogin_NetworkError_Propagates(t *testing.T) {
	store := secrets.NewMemStore()
	require.NoError(t, store.Set("p", "refresh", "rt"))
	require.NoError(t, store.Set("p", "password", "pw"))

	svc := &fakeLoginRefresher{refreshErr: errors.New("dial tcp: connection refused")}

	_, err := RefreshOrRelogin(context.Background(), store, svc, "p", "a@b.c")
	require.Error(t, err)
	assert.Equal(t, CodeNetworkError, errCode(t, err))
	assert.Equal(t, 0, svc.loginCalls)
}
