package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/cli/internal/cmdutil"
	"github.com/Tencent/WeKnora/cli/internal/config"
	"github.com/Tencent/WeKnora/cli/internal/iostreams"
	"github.com/Tencent/WeKnora/cli/internal/prompt"
	"github.com/Tencent/WeKnora/cli/internal/secrets"
	sdk "github.com/Tencent/WeKnora/client"
)

// fakeRegisterService captures the register request and scripts register/login
// responses.
type fakeRegisterService struct {
	regResp   *sdk.RegisterResponse
	regErr    error
	loginResp *sdk.LoginResponse
	loginErr  error

	gotReg     sdk.RegisterRequest
	loginCalls int
}

func (f *fakeRegisterService) Register(_ context.Context, req sdk.RegisterRequest) (*sdk.RegisterResponse, error) {
	f.gotReg = req
	return f.regResp, f.regErr
}

func (f *fakeRegisterService) Login(_ context.Context, _ sdk.LoginRequest) (*sdk.LoginResponse, error) {
	f.loginCalls++
	return f.loginResp, f.loginErr
}

func TestRunRegister_Success_CreatesAccountLogsInAndPersists(t *testing.T) {
	iostreams.SetForTest(t)
	f, store := newTestFactoryWithConfig(t, scriptedPrompter{email: "alice@example.com", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})

	svc := &fakeRegisterService{
		regResp: &sdk.RegisterResponse{Success: true, Message: "Registration successful",
			User: &sdk.AuthUser{ID: "u1", Email: "alice@example.com"}},
		loginResp: &sdk.LoginResponse{Success: true, Token: "jwt-access", RefreshToken: "jwt-refresh",
			User: &sdk.AuthUser{ID: "u1", Email: "alice@example.com", TenantID: 7}},
	}
	opts := &RegisterOptions{Remember: true}
	require.NoError(t, runRegister(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc))

	// Username derived from the email local part.
	assert.Equal(t, "alice", svc.gotReg.Username)
	assert.Equal(t, "alice@example.com", svc.gotReg.Email)
	assert.Equal(t, "secret1", svc.gotReg.Password)
	assert.Equal(t, 1, svc.loginCalls, "register must auto-login")

	acc, _ := store.Get("prod", "access")
	ref, _ := store.Get("prod", "refresh")
	pw, _ := store.Get("prod", "password")
	assert.Equal(t, "jwt-access", acc)
	assert.Equal(t, "jwt-refresh", ref)
	assert.Equal(t, "secret1", pw, "password remembered by default")

	cfg, _ := f.Config()
	assert.Equal(t, "alice@example.com", cfg.Profiles["prod"].User)
	assert.NotEmpty(t, cfg.Profiles["prod"].TokenRef)
	assert.Equal(t, "https://kb.example.com", cfg.Profiles["prod"].Host, "host preserved")
}

func TestRunRegister_UsernameOverride(t *testing.T) {
	iostreams.SetForTest(t)
	f, _ := newTestFactoryWithConfig(t, scriptedPrompter{email: "alice@example.com", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})
	svc := &fakeRegisterService{
		regResp:   &sdk.RegisterResponse{Success: true},
		loginResp: &sdk.LoginResponse{Success: true, Token: "t", RefreshToken: "r"},
	}
	opts := &RegisterOptions{Username: "custom-name", Remember: true}
	require.NoError(t, runRegister(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc))
	assert.Equal(t, "custom-name", svc.gotReg.Username)
}

func TestRunRegister_InviteOnly(t *testing.T) {
	iostreams.SetForTest(t)
	f, _ := newTestFactoryWithConfig(t, scriptedPrompter{email: "a@b.c", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})
	svc := &fakeRegisterService{regErr: errors.New(`HTTP error 403: {"code":1002,"message":"Registration is invite-only"}`)}

	err := runRegister(context.Background(), &RegisterOptions{}, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc)
	require.Error(t, err)
	var typed *cmdutil.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, cmdutil.CodeAuthForbidden, typed.Code)
	assert.Equal(t, 0, svc.loginCalls, "must not attempt login when registration is disabled")
}

func TestRunRegister_EmailAlreadyExists_SurfacesServerMessage(t *testing.T) {
	iostreams.SetForTest(t)
	f, _ := newTestFactoryWithConfig(t, scriptedPrompter{email: "a@b.c", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})
	svc := &fakeRegisterService{regErr: errors.New(`HTTP error 400: {"code":1000,"message":"email already exists"}`)}

	err := runRegister(context.Background(), &RegisterOptions{}, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc)
	require.Error(t, err)
	var typed *cmdutil.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, cmdutil.CodeInputInvalidArgument, typed.Code)
	assert.Contains(t, typed.Message, "email already exists")
	assert.Contains(t, typed.Hint, "auth login")
}

func TestRunRegister_AutoLoginFails(t *testing.T) {
	iostreams.SetForTest(t)
	f, _ := newTestFactoryWithConfig(t, scriptedPrompter{email: "a@b.c", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})
	svc := &fakeRegisterService{
		regResp:   &sdk.RegisterResponse{Success: true},
		loginResp: &sdk.LoginResponse{Success: false, Message: "no"},
	}
	err := runRegister(context.Background(), &RegisterOptions{}, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc)
	require.Error(t, err)
	var typed *cmdutil.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, cmdutil.CodeAuthBadCredential, typed.Code)
}

func TestRunRegister_NoRemember_DoesNotStorePassword(t *testing.T) {
	iostreams.SetForTest(t)
	f, store := newTestFactoryWithConfig(t, scriptedPrompter{email: "a@b.c", password: "secret1"})
	seedActiveProfile(t, "prod", config.Profile{Host: "https://kb.example.com"})
	svc := &fakeRegisterService{
		regResp:   &sdk.RegisterResponse{Success: true},
		loginResp: &sdk.LoginResponse{Success: true, Token: "t", RefreshToken: "r"},
	}
	require.NoError(t, runRegister(context.Background(), &RegisterOptions{Remember: false}, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, svc))
	_, perr := store.Get("prod", "password")
	assert.ErrorIs(t, perr, secrets.ErrNotFound)
}

func TestRunRegister_NoActiveProfile_Errors(t *testing.T) {
	iostreams.SetForTest(t)
	f, _ := newTestFactoryWithConfig(t, prompt.AgentPrompter{})
	err := runRegister(context.Background(), &RegisterOptions{}, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, f, nil)
	require.Error(t, err)
	var typed *cmdutil.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, cmdutil.CodeAuthUnauthenticated, typed.Code)
}

func TestDeriveUsername(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"alice@example.com", "alice"},
		{"a.b+tag@ex.co", "a.b+tag"},
		{"nodomain", "nodomain"},
		{"x@y.z", "x@y.z"}, // local part too short → fall back to full email
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, deriveUsername(tc.email), "email=%q", tc.email)
	}
}

func TestNewCmdRegister_Flags(t *testing.T) {
	f, _ := newTestFactoryWithConfig(t, prompt.AgentPrompter{})
	cmd := NewCmdRegister(f, nil)
	assert.NotNil(t, cmd.Flags().Lookup("username"))
	assert.NotNil(t, cmd.Flags().Lookup("remember"))
	assert.Nil(t, cmd.Flags().Lookup("host"), "auth register must not declare --host")
	assert.Nil(t, cmd.Flags().Lookup("name"), "auth register must not declare --name")
}
