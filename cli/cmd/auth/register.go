package auth

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tencent/WeKnora/cli/internal/cmdutil"
	"github.com/Tencent/WeKnora/cli/internal/iostreams"
	sdk "github.com/Tencent/WeKnora/client"
)

// authRegisterFields enumerates the fields surfaced for `--format json`
// discovery on `auth register`. Same shape as `auth login` because register
// delegates to login once the account is created.
var authRegisterFields = []string{
	"profile", "host", "mode", "user", "tenant_id",
}

// RegisterOptions captures flags + prompts for `auth register`. Host and
// Profile are resolved from the active profile in config (like `auth login`).
type RegisterOptions struct {
	Host     string // resolved from the active profile's Host in config
	Profile  string // resolved active profile name (honors global --profile)
	Email    string
	Password string
	// Username is optional; when empty it is derived from the email local part.
	Username string
	// Remember persists the password for an automatic re-login (--remember,
	// default true).
	Remember bool
}

// RegisterService is the narrow SDK surface auth register depends on: account
// creation (no tokens) plus the follow-up login that yields them. *sdk.Client
// satisfies both.
type RegisterService interface {
	Register(ctx context.Context, req sdk.RegisterRequest) (*sdk.RegisterResponse, error)
	Login(ctx context.Context, req sdk.LoginRequest) (*sdk.LoginResponse, error)
}

// NewCmdRegister builds `weknora auth register`. runF is the testable
// entrypoint (left nil for production; see register_test.go).
func NewCmdRegister(f *cmdutil.Factory, runF func(context.Context, *RegisterOptions, *cmdutil.FormatOptions, *cmdutil.Factory, RegisterService) error) *cobra.Command {
	opts := &RegisterOptions{Remember: true}
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a new account on the active profile's server and log in",
		Long: `Create a new user account on the active profile's server, then log in
automatically with the same email + password.

Registration targets the active profile (override with the global --profile
flag); create the profile first with
` + "`weknora profile add <name> --host <h> --use`" + `.

The username defaults to the part of the email before "@" (override with
--username). The password is remembered (--remember, opt out with
--remember=false) so a later expired credential re-authenticates automatically.

If the server has registration disabled (invite-only) the command fails with
auth.forbidden; if the email/username is already taken it fails with
input.invalid_argument — use ` + "`weknora auth login`" + ` for an existing account.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fopts, err := cmdutil.CheckFormatFlag(c)
			if err != nil {
				return err
			}
			fopts.ResolveDefault(iostreams.IO.IsStdoutTTY())
			if runF != nil {
				return runF(c.Context(), opts, fopts, f, nil)
			}
			return runRegister(c.Context(), opts, fopts, f, nil)
		},
	}
	cmd.Flags().StringVar(&opts.Username, "username", "", "Username (default: the part of the email before '@')")
	cmd.Flags().BoolVar(&opts.Remember, "remember", true, "Remember the password so an expired credential re-authenticates automatically (--remember=false to opt out)")
	cmdutil.AddFormatFlag(cmd, authRegisterFields...)
	cmdutil.SetAgentHelp(cmd, cmdutil.AgentHelp{
		UsedFor: "create a new account on the active profile's server and log in automatically. Fails with auth.forbidden when registration is invite-only.",
		Output:  "envelope.data is {profile, host, mode, user, tenant_id} on success",
		Warnings: []string{
			"a profile must exist and be active first (`weknora profile add <n> --host <url> --use`)",
			"email + password are prompted interactively (no flags)",
			"the username defaults to the email local part — pass --username to override on a collision",
			"the password is remembered by default (--remember=false to disable); it is stored in the keyring (or a 0600 file)",
		},
	})
	return cmd
}

// registerServiceFor returns a fresh SDK client targeting host. Like
// loginServiceFor, it cannot reuse Factory.Client (that closure requires an
// already-authenticated profile).
func registerServiceFor(host string) RegisterService {
	if host == "" {
		return nil
	}
	return sdk.NewClient(host)
}

func runRegister(ctx context.Context, opts *RegisterOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, svc RegisterService) error {
	name, host, err := resolveActiveProfile(f)
	if err != nil {
		return err
	}
	opts.Host, opts.Profile = host, name
	if svc == nil {
		svc = registerServiceFor(host)
	}

	if err := validateHost(opts.Host); err != nil {
		return err
	}
	// Reject shell-metacharacter / path-like names up-front so opts.Profile
	// stays safe to interpolate into the keyring namespace and config keys.
	if err := cmdutil.ValidateProfileName(opts.Profile); err != nil {
		return err
	}

	// Interactive email + password prompts (same UX as `auth login`).
	if opts.Email == "" || opts.Password == "" {
		p := f.Prompter()
		if opts.Email == "" {
			email, err := p.Input("Email", "")
			if err != nil {
				return cmdutil.Wrapf(cmdutil.CodeInputMissingFlag, err, "email prompt")
			}
			opts.Email = email
		}
		if opts.Password == "" {
			pw, err := p.Password("Password")
			if err != nil {
				return cmdutil.Wrapf(cmdutil.CodeInputMissingFlag, err, "password prompt")
			}
			opts.Password = pw
		}
	}
	opts.Email = strings.TrimSpace(opts.Email)
	if opts.Email == "" {
		return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "email is required to register")
	}
	if opts.Password == "" {
		return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "password is required to register")
	}

	username := opts.Username
	if username == "" {
		username = deriveUsername(opts.Email)
	}

	resp, err := svc.Register(ctx, sdk.RegisterRequest{
		Username: username,
		Email:    opts.Email,
		Password: opts.Password,
	})
	if err != nil {
		return classifyRegisterError(err)
	}
	if resp == nil || !resp.Success {
		msg := "registration refused by the server"
		if resp != nil && resp.Message != "" {
			msg = "registration refused: " + resp.Message
		}
		return cmdutil.NewError(cmdutil.CodeOperationFailed, msg)
	}

	// The register endpoint issues no tokens, so log in to obtain a session.
	loginResp, err := svc.Login(ctx, sdk.LoginRequest{Email: opts.Email, Password: opts.Password})
	if err != nil {
		if cmdutil.ClassifyHTTPError(err) == cmdutil.CodeNetworkError {
			return cmdutil.Wrapf(cmdutil.CodeNetworkError, err, "auto-login after register: check host reachability")
		}
		return cmdutil.Wrapf(cmdutil.CodeAuthBadCredential, err, "auto-login after register")
	}
	if loginResp == nil || !loginResp.Success || loginResp.Token == "" {
		return cmdutil.NewError(cmdutil.CodeAuthBadCredential,
			"account created but auto-login failed; run `weknora auth login`")
	}

	loginOpts := &LoginOptions{
		Host:     opts.Host,
		Profile:  opts.Profile,
		Email:    opts.Email,
		Password: opts.Password,
		Remember: opts.Remember,
	}
	return persistBearer(loginOpts, fopts, f, loginResp, "Registered and logged in")
}

// deriveUsername turns an email into a candidate username: the local part
// (before '@'), clamped to the server's 2..50 length bound. Falls back to the
// full email when the local part is too short to satisfy the minimum.
func deriveUsername(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	if len(local) > 50 {
		local = local[:50]
	}
	if len(local) < 2 {
		local = email
		if len(local) > 50 {
			local = local[:50]
		}
	}
	return local
}

// classifyRegisterError maps the SDK's "HTTP error <status>: <body>" error into
// a typed CLI error with an actionable hint.
func classifyRegisterError(err error) error {
	switch cmdutil.ClassifyHTTPError(err) {
	case cmdutil.CodeNetworkError:
		return cmdutil.Wrapf(cmdutil.CodeNetworkError, err, "register: check host reachability")
	case cmdutil.CodeAuthForbidden:
		return &cmdutil.Error{
			Code:    cmdutil.CodeAuthForbidden,
			Message: "registration is disabled on this server (invite-only)",
			Hint:    "ask an administrator for an invite, or log in with an existing account: `weknora auth login`",
			Cause:   err,
		}
	case cmdutil.CodeResourceAlreadyExists:
		return registerTaken(err, cmdutil.CodeResourceAlreadyExists)
	case cmdutil.CodeInputInvalidArgument:
		// The public register endpoint returns 400 for a duplicate email or an
		// already-taken username, as well as for other validation failures.
		return registerTaken(err, cmdutil.CodeInputInvalidArgument)
	default:
		return cmdutil.WrapHTTP(err, "register")
	}
}

// registerTaken builds the error for a refused registration, best-effort
// surfacing the server's own message (e.g. "email already exists").
func registerTaken(err error, code cmdutil.ErrorCode) error {
	msg := serverMessage(err)
	if msg == "" {
		msg = "registration failed (the email or username may already be in use)"
	}
	return &cmdutil.Error{
		Code:    code,
		Message: msg,
		Hint:    "if this account already exists, log in with `weknora auth login`; otherwise pick a different username with `--username`",
		Cause:   err,
	}
}

// serverMessage extracts the AppError "message" field from an SDK error of the
// form `HTTP error <status>: {"code":N,"message":"..."}`. Returns "" when the
// body is absent or not the expected JSON envelope.
func serverMessage(err error) string {
	msg := err.Error()
	i := strings.Index(msg, ": ")
	if i < 0 {
		return ""
	}
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(msg[i+2:]), &envelope) != nil {
		return ""
	}
	return envelope.Message
}
