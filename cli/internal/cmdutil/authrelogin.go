package cmdutil

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/cli/internal/secrets"
	sdk "github.com/Tencent/WeKnora/client"
)

// LoginRefresher is the SDK surface needed to renew a profile's credentials:
// the OAuth refresh grant plus a full email+password login. *sdk.Client
// satisfies it implicitly.
type LoginRefresher interface {
	RefreshToken(ctx context.Context, refreshToken string) (*sdk.RefreshTokenResponse, error)
	Login(ctx context.Context, req sdk.LoginRequest) (*sdk.LoginResponse, error)
}

// RefreshOrRelogin renews the access token for profileName.
//
// It first attempts the standard refresh-token grant (RefreshAndPersist).
// Only when that fails because the refresh credential is missing or rejected
// (auth.token_expired) AND the CLI remembered the account password does it
// fall back to a full password login, persisting the fresh token pair. This is
// what makes "enter the password once and never again" hold across the 7-day
// refresh-token window.
//
// email is the account email (normally the profile's persisted User). When no
// password is remembered, or email is empty, the ORIGINAL auth.token_expired
// error is returned unchanged so the caller still surfaces the familiar
// "run `weknora auth login`" hint.
func RefreshOrRelogin(
	ctx context.Context,
	store secrets.Store,
	svc LoginRefresher,
	profileName, email string,
) (string, error) {
	token, err := RefreshAndPersist(ctx, store, svc, profileName)
	if err == nil {
		return token, nil
	}
	// Only a fully-expired refresh credential is recoverable via re-login;
	// network / server / forbidden errors propagate as-is.
	if !IsAuthExpired(err) {
		return "", err
	}

	pw, perr := store.Get(profileName, "password")
	if errors.Is(perr, secrets.ErrNotFound) || pw == "" {
		return "", err
	}
	if perr != nil {
		return "", Wrapf(CodeLocalKeychainDenied, perr, "load saved password")
	}
	if email == "" {
		return "", err
	}

	resp, lerr := svc.Login(ctx, sdk.LoginRequest{Email: email, Password: pw})
	if lerr != nil {
		// A transport failure is transient - keep the saved password so a
		// later invocation can retry. A definitive rejection (wrong / changed
		// password) means the stored credential is stale: drop it so the user
		// is asked to re-authenticate instead of silently failing forever.
		if ClassifyHTTPError(lerr) == CodeNetworkError {
			return "", Wrapf(CodeNetworkError, lerr, "auto re-login: check host reachability")
		}
		_ = store.Delete(profileName, "password")
		return "", err
	}
	if resp == nil || !resp.Success || resp.Token == "" {
		_ = store.Delete(profileName, "password")
		return "", err
	}

	if serr := store.Set(profileName, "access", resp.Token); serr != nil {
		return "", Wrapf(CodeLocalKeychainDenied, serr, "save access token")
	}
	if resp.RefreshToken != "" {
		if serr := store.Set(profileName, "refresh", resp.RefreshToken); serr != nil {
			return "", Wrapf(CodeLocalKeychainDenied, serr, "save refresh token")
		}
	}
	return resp.Token, nil
}
