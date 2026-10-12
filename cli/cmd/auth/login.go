package auth

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tencent/WeKnora/cli/internal/cmdutil"
	"github.com/Tencent/WeKnora/cli/internal/config"
	"github.com/Tencent/WeKnora/cli/internal/iostreams"
	"github.com/Tencent/WeKnora/cli/internal/secrets"
	sdk "github.com/Tencent/WeKnora/client"
)

// authLoginFields enumerates the fields surfaced for `--format json` discovery on
// `auth login`. The post-login summary has no token values - they stay in the
// keyring; agents who need to verify the credential should re-run
// `auth status`.
var authLoginFields = []string{
	"profile", "host", "mode", "user", "tenant_id",
}

// LoginOptions is the configuration captured from flags + prompts. Host and
// Profile are resolved from the active profile in config (not from flags) -
// `auth login` authenticates the already-existing active profile, created
// beforehand with `weknora profile add <name> --host <h> --use`.
type LoginOptions struct {
	Host      string // resolved from the active profile's Host in config
	Profile   string // resolved active profile name (honors global --profile)
	WithToken bool   // --with-token: read api key from stdin instead of prompting password
	APIKey    string // populated by --with-token from stdin
	Email     string // --email, or prompted when empty
	Password  string // --password, or prompted when empty
	// PasswordStdin reads the password from stdin instead of --password, so it
	// need not appear on the command line (docker-login style). Mutually
	// exclusive with --password and --with-token.
	PasswordStdin bool
	// Remember persists the password to the secrets store so a fully-expired
	// refresh token can be recovered by an automatic re-login (--remember,
	// default true). Ignored on the --with-token path.
	Remember    bool
	StdinReader io.Reader // override for tests
}

// LoginService is the narrow SDK surface auth login depends on.
// *sdk.Client satisfies it implicitly via the new Login(ctx, LoginRequest)
// signature added in client/auth.go.
type LoginService interface {
	Login(ctx context.Context, req sdk.LoginRequest) (*sdk.LoginResponse, error)
}

// apiKeyValidator probes /auth/me with the supplied API key so a bad key
// fails fast at `auth login --with-token` time rather than on the next
// authenticated call.
//
// Returns the resolved user (used to populate Profile.User / TenantID at
// rest, so later `auth list` reflects who owns the key).
type apiKeyValidator func(ctx context.Context, host, apiKey string) (*sdk.AuthUser, error)

// defaultAPIKeyValidator builds a one-shot SDK client with the supplied key
// and calls /auth/me. Side-effect-free; no persistence.
var defaultAPIKeyValidator apiKeyValidator = func(ctx context.Context, host, apiKey string) (*sdk.AuthUser, error) {
	resp, err := sdk.NewClient(host, sdk.WithAPIKey(apiKey)).GetCurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !resp.Success || resp.Data.User == nil {
		return nil, fmt.Errorf("server rejected the API key (no user returned)")
	}
	return resp.Data.User, nil
}

// NewCmdLogin builds the `weknora auth login` command. runF is the testable
// entrypoint (left nil for production; see cli/cmd/auth/login_test.go).
func NewCmdLogin(f *cmdutil.Factory, runF func(context.Context, *LoginOptions, *cmdutil.FormatOptions, *cmdutil.Factory, LoginService) error) *cobra.Command {
	opts := &LoginOptions{Remember: true}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate the active profile against its WeKnora server",
		Long: `Authenticate the active profile by email + password or pipe an API key
with --with-token.

Email + password may be entered interactively (prompted when omitted) or
supplied non-interactively with --email and --password, so the command works in
environments without a TTY (CI, agents, containers). Supplying only one of the
two flags prompts for the other. To keep the password out of the process list
and shell history, pipe it on stdin with --password-stdin instead of --password.

` + "`auth login`" + ` operates on the active profile (override with the global
--profile flag). Create the profile first with
` + "`weknora profile add <name> --host <h> --use`" + `, then run ` + "`weknora auth login`" + `.

Credentials are persisted to the OS keyring when available; otherwise to a
0600 file under $XDG_CONFIG_HOME/weknora/secrets.

By default the password is remembered too (--remember, opt out with
--remember=false): when the 7-day refresh token finally expires, the CLI
re-authenticates automatically instead of prompting again.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			fopts, err := cmdutil.CheckFormatFlag(c)
			if err != nil {
				return err
			}
			fopts.ResolveDefault(iostreams.IO.IsStdoutTTY())
			if opts.StdinReader == nil {
				opts.StdinReader = iostreams.IO.In
			}
			if runF != nil {
				// Test seam: the injected runF supplies its own service and
				// (where needed) seeds opts.Host / opts.Profile itself.
				return runF(c.Context(), opts, fopts, f, nil)
			}
			return runLogin(c.Context(), opts, fopts, f, nil)
		},
	}
	cmd.Flags().BoolVar(&opts.WithToken, "with-token", false, "Read an API key from stdin instead of prompting for password")
	cmd.Flags().StringVar(&opts.Email, "email", "", "Account email (non-interactive; skips the email prompt)")
	cmd.Flags().StringVar(&opts.Password, "password", "", "Account password (non-interactive; skips the password prompt). WARNING: visible in the process list / shell history")
	cmd.Flags().BoolVar(&opts.PasswordStdin, "password-stdin", false, "Read the account password from stdin (keeps it out of the process list / shell history)")
	cmd.Flags().BoolVar(&opts.Remember, "remember", true, "Remember the password so an expired credential re-authenticates automatically (--remember=false to opt out)")
	cmdutil.AddFormatFlag(cmd, authLoginFields...)
	cmdutil.SetAgentHelp(cmd, cmdutil.AgentHelp{
		UsedFor:  "authenticate the active profile; --with-token reads an API key from stdin, --email/--password/--password-stdin log in non-interactively",
		Examples: []string{
			`echo "$WEKNORA_API_KEY" | weknora auth login --with-token`,
			`weknora auth login --email you@example.com --password "$WEKNORA_PASSWORD"`,
			`echo "$WEKNORA_PASSWORD" | weknora auth login --email you@example.com --password-stdin`,
		},
		Output: "envelope.data is {profile, host, mode, user, tenant_id} on success",
		Warnings: []string{
			"a profile must exist and be active first (`weknora profile add <n> --host <url> --use`)",
			"password login works non-interactively via --email + (--password | --password-stdin), or interactively when both are omitted (a TTY is required then)",
			"--password on the command line is visible in the process list / shell history — prefer `--password-stdin` (pipe it) or `--with-token` on shared hosts",
			"--password, --password-stdin and --with-token are mutually exclusive",
			"the password is remembered by default (--remember=false to disable); it is stored in the keyring (or a 0600 file) and reused to re-login automatically once the refresh token expires",
		},
	})
	return cmd
}

// resolveActiveProfile loads config and resolves the active profile (honoring
// the global --profile override) plus its persisted Host. `auth login`
// authenticates an already-existing profile; profile creation is `profile
// add`'s job. Returns typed errors when no active profile is configured or
// the profile lacks a host.
func resolveActiveProfile(f *cmdutil.Factory) (name, host string, err error) {
	// f.Config() already folds the global --profile / WEKNORA_PROFILE override
	// into cfg.CurrentProfile (same source f.ActiveProfile reads), so one load
	// resolves the active profile — matches logout/refresh.
	cfg, err := f.Config()
	if err != nil {
		return "", "", err
	}
	active := cfg.CurrentProfile
	if active == "" {
		return "", "", cmdutil.NewError(cmdutil.CodeAuthUnauthenticated,
			"no active profile; run `weknora profile add <name> --host <h> --use` first")
	}
	prof, ok := cfg.Profiles[active]
	if !ok {
		return "", "", cmdutil.NewError(cmdutil.CodeLocalProfileNotFound,
			fmt.Sprintf("active profile %q not found in config", active))
	}
	if prof.Host == "" {
		return "", "", cmdutil.NewError(cmdutil.CodeLocalConfigCorrupt,
			fmt.Sprintf("profile %q has no host", active))
	}
	return active, prof.Host, nil
}

// loginServiceFor returns a fresh SDK client targeting host. login.go cannot
// reuse Factory.Client because that closure requires an existing profile.
func loginServiceFor(host string) LoginService {
	if host == "" {
		return nil
	}
	return sdk.NewClient(host)
}

func runLogin(ctx context.Context, opts *LoginOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, svc LoginService) error {
	// Resolve the target profile + host from the active profile in config.
	// `auth login` no longer takes --host / --name; it authenticates the
	// already-existing active profile (override via the global --profile).
	name, host, err := resolveActiveProfile(f)
	if err != nil {
		return err
	}
	opts.Profile = name
	opts.Host = host
	if svc == nil {
		svc = loginServiceFor(host)
	}

	if err := validateHost(opts.Host); err != nil {
		return err
	}
	// Reject shell-metacharacter / path-like names up-front so opts.Profile
	// stays safe to interpolate into the keyring namespace, config.yaml
	// keys, and (later) envelope.error.retry_command. Matches `profile add`.
	if err := cmdutil.ValidateProfileName(opts.Profile); err != nil {
		return err
	}

	// --with-token (API key) and --email/--password (JWT) are two distinct
	// credential modes; mixing them is a user error rather than a silent
	// "API key wins" (which would store a key the user didn't intend to use).
	if opts.WithToken && (opts.Email != "" || opts.Password != "" || opts.PasswordStdin) {
		return cmdutil.NewError(cmdutil.CodeInputInvalidArgument,
			"--with-token cannot be combined with --email/--password/--password-stdin")
	}
	// Two sources for the same value; require the caller to pick one so the
	// password is never silently taken from the wrong place.
	if opts.Password != "" && opts.PasswordStdin {
		return cmdutil.NewError(cmdutil.CodeInputInvalidArgument,
			"--password and --password-stdin are mutually exclusive")
	}

	if opts.WithToken {
		key, err := readStdinTrimmed(opts.StdinReader)
		if err != nil {
			return cmdutil.Wrapf(cmdutil.CodeLocalFileIO, err, "read stdin")
		}
		if key == "" {
			return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "--with-token requires an API key piped to stdin")
		}
		opts.APIKey = key
		// Validate against the server before persisting so a typo'd /
		// expired / wrong-host key fails fast at login time. The probe
		// is /auth/me - read-only, side-effect-free.
		user, err := defaultAPIKeyValidator(ctx, opts.Host, key)
		if err != nil {
			// Transport errors (connection refused, DNS failure) must not be
			// surfaced as auth.bad_credential — the key may be fine but the
			// host is unreachable. Classify via WrapHTTP so network errors
			// get CodeNetworkError and the hint points at `weknora doctor`.
			if cmdutil.ClassifyHTTPError(err) == cmdutil.CodeNetworkError {
				return cmdutil.Wrapf(cmdutil.CodeNetworkError, err, "validate API key: check host reachability")
			}
			return cmdutil.Wrapf(cmdutil.CodeAuthBadCredential, err, "validate API key")
		}
		return persistAPIKey(opts, fopts, f, user)
	}

	// --password-stdin: read the password from stdin (docker-login style) so it
	// never appears in the process list / shell history. Done before the prompt
	// block so an empty stdin fails fast instead of falling through to a prompt.
	if opts.PasswordStdin {
		pw, err := readStdinTrimmed(opts.StdinReader)
		if err != nil {
			return cmdutil.Wrapf(cmdutil.CodeLocalFileIO, err, "read password from stdin")
		}
		if pw == "" {
			return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "--password-stdin requires the password piped to stdin")
		}
		opts.Password = pw
	}

	// Interactive: prompt for whatever --email/--password did not supply.
	// svc is always set by now (loginServiceFor returns non-nil because
	// resolveActiveProfile guarantees a non-empty host).
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
	// Non-interactive callers (no TTY) reach here with the prompt having
	// already errored; this guards the flag path against an empty --email
	// "  " / empty --password that would otherwise be sent to the server.
	opts.Email = strings.TrimSpace(opts.Email)
	if opts.Email == "" {
		return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "email is required; pass --email or run interactively at a terminal")
	}
	if opts.Password == "" {
		return cmdutil.NewError(cmdutil.CodeInputMissingFlag, "password is required; pass --password or run interactively at a terminal")
	}

	resp, err := svc.Login(ctx, sdk.LoginRequest{Email: opts.Email, Password: opts.Password})
	if err != nil {
		// Transport errors (connection refused, DNS failure) must not be
		// surfaced as auth.bad_credential — credentials may be fine but the
		// host is unreachable. Classify so network errors get CodeNetworkError.
		if cmdutil.ClassifyHTTPError(err) == cmdutil.CodeNetworkError {
			return cmdutil.Wrapf(cmdutil.CodeNetworkError, err, "login: check host reachability")
		}
		return cmdutil.Wrapf(cmdutil.CodeAuthBadCredential, err, "login")
	}
	if !resp.Success || resp.Token == "" {
		return cmdutil.NewError(cmdutil.CodeAuthBadCredential, fmt.Sprintf("login refused: %s", resp.Message))
	}

	return persistJWT(opts, fopts, f, resp)
}

// persistAPIKey saves the --with-token API key and writes the profile.
// user is the principal returned by /auth/me during pre-persist validation,
// used to populate Profile.User / TenantID so `auth list` reflects who
// owns the key.
func persistAPIKey(opts *LoginOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, user *sdk.AuthUser) error {
	store, err := f.Secrets()
	if err != nil {
		return err
	}
	warnOnFileFallback(store)
	if err := store.Set(opts.Profile, "api_key", opts.APIKey); err != nil {
		return cmdutil.Wrapf(cmdutil.CodeLocalKeychainDenied, err, "save api key")
	}
	// Switching to an API key: drop any remembered password so it can't be
	// spent on a later auto re-login against a different credential mode.
	_ = store.Delete(opts.Profile, "password")
	// Switching credential mode: this profile is now an API-key profile, so
	// any leftover JWT refs (from a prior password login) are stale. Set the
	// api-key ref and clear the token/refresh refs; keep Host + existing
	// User/TenantID (merged in saveProfileRef via applyUser).
	mutate := func(prof *config.Profile) {
		prof.APIKeyRef = store.Ref(opts.Profile, "api_key")
		prof.TokenRef = ""
		prof.RefreshRef = ""
		applyUser(prof, user)
	}
	return saveProfileRef(opts, fopts, f, mutate, ModeAPIKey, user, "Logged in")
}

// persistJWT saves access + refresh tokens and writes the profile.
func persistJWT(opts *LoginOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, resp *sdk.LoginResponse) error {
	return persistBearer(opts, fopts, f, resp, "Logged in")
}

// persistBearer saves the access + refresh tokens (and, when opts.Remember, the
// password for a later automatic re-login) and merges the credential refs into
// the active profile. verb prefixes the human success line — "Logged in" for
// `auth login`, "Registered and logged in" for `auth register`; the JSON
// payload is identical for both flows.
func persistBearer(opts *LoginOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, resp *sdk.LoginResponse, verb string) error {
	store, err := f.Secrets()
	if err != nil {
		return err
	}
	warnOnFileFallback(store)
	if err := store.Set(opts.Profile, "access", resp.Token); err != nil {
		return cmdutil.Wrapf(cmdutil.CodeLocalKeychainDenied, err, "save access token")
	}
	if resp.RefreshToken != "" {
		if err := store.Set(opts.Profile, "refresh", resp.RefreshToken); err != nil {
			return cmdutil.Wrapf(cmdutil.CodeLocalKeychainDenied, err, "save refresh token")
		}
	}
	// Remember the password (opt-out via --remember=false) so a fully-expired
	// refresh token can be recovered by an automatic re-login instead of
	// prompting again. When not remembering, clear any previously-stored
	// password so the opt-out takes effect immediately.
	if opts.Remember && opts.Password != "" {
		if err := store.Set(opts.Profile, "password", opts.Password); err != nil {
			return cmdutil.Wrapf(cmdutil.CodeLocalKeychainDenied, err, "save password")
		}
	} else {
		_ = store.Delete(opts.Profile, "password")
	}
	// Switching to a JWT profile: set token refs and drop any stale api-key
	// ref; keep Host + existing User/TenantID (merged in saveProfileRef via applyUser).
	mutate := func(prof *config.Profile) {
		prof.TokenRef = store.Ref(opts.Profile, "access")
		prof.RefreshRef = store.Ref(opts.Profile, "refresh")
		prof.APIKeyRef = ""
		applyUser(prof, resp.User)
	}
	return saveProfileRef(opts, fopts, f, mutate, ModeBearer, resp.User, verb)
}

// applyUser overwrites prof.User / prof.TenantID only when the server actually
// returned them. A nil user (or empty email) must NOT wipe an existing User
// that was set during `profile add` or a prior login.
func applyUser(prof *config.Profile, user *sdk.AuthUser) {
	if user == nil {
		return
	}
	if user.Email != "" {
		prof.User = user.Email
	}
	if user.TenantID != 0 {
		prof.TenantID = user.TenantID
	}
}

// loginResult is the typed payload emitted by `--format json`. mode is derived from
// whether the server returned a user (password flow) vs API-key flow.
type loginResult struct {
	Profile  string `json:"profile"`
	Host     string `json:"host"`
	Mode     string `json:"mode"` // ModeBearer or ModeAPIKey
	User     string `json:"user,omitempty"`
	TenantID uint64 `json:"tenant_id,omitempty"`
}

// saveProfileRef MERGES the new credential refs into the EXISTING profile
// record and prints success. The active profile already exists (created via
// `profile add`, carrying Host and possibly User/TenantID); mutate sets only
// the credential refs + any server-returned user, so re-login never clobbers
// the host or wipes an existing user. cfg.CurrentProfile is left untouched -
// `auth login` authenticates the already-active profile, it doesn't switch.
// verb prefixes the human success line ("Logged in" / "Registered and logged in").
func saveProfileRef(opts *LoginOptions, fopts *cmdutil.FormatOptions, f *cmdutil.Factory, mutate func(*config.Profile), mode string, user *sdk.AuthUser, verb string) error {
	cfg, err := f.Config()
	if err != nil {
		return err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	prof := cfg.Profiles[opts.Profile] // existing record (Host, User, TenantID)
	mutate(&prof)
	cfg.Profiles[opts.Profile] = prof
	if err := config.Save(cfg); err != nil {
		return cmdutil.Wrapf(cmdutil.CodeLocalFileIO, err, "save config")
	}
	if fopts.WantsJSON() {
		// mode reflects the credential actually stored (passed by the caller),
		// not whether the server happened to return a user — an API-key login
		// validates via /auth/me and DOES get a user, so deriving mode from
		// user != nil would mislabel it as bearer.
		result := loginResult{Profile: opts.Profile, Host: opts.Host, Mode: mode}
		if user != nil {
			result.User = user.Email
			result.TenantID = user.TenantID
		}
		return fopts.Emit(iostreams.IO.Out, result, nil)
	}
	who := opts.Profile
	if user != nil {
		who = user.Email
	}
	fmt.Fprintf(iostreams.IO.Out, "✓ %s to %s as %s (profile=%s)\n", verb, opts.Host, who, opts.Profile)
	return nil
}

// validateHost rejects empty / non-http URLs early so we surface a clean
// flag error instead of failing inside the SDK transport.
func validateHost(host string) error {
	_, err := cmdutil.NormalizeHost(host)
	return err
}

// warnOnFileFallback prints a one-shot stderr advisory when the secrets
// store fell back to the plaintext 0600 file backend (keychain unavailable
// - typical on headless CI, WSL without DBus, agent containers). Helps
// users notice that credentials are NOT in the OS keychain before they're
// surprised by it later. doctor's credential_storage check carries the
// same info but agents that bypass doctor would otherwise miss it.
func warnOnFileFallback(store secrets.Store) {
	if _, isFile := store.(*secrets.FileStore); !isFile {
		return
	}
	fmt.Fprintln(iostreams.IO.Err, "warning: OS keychain unavailable - credentials will be saved to a 0600 file under $XDG_CONFIG_HOME/weknora/secrets/.")
	fmt.Fprintln(iostreams.IO.Err, "         install / unlock the keyring (or use `weknora doctor` to inspect) for OS-backed storage.")
}

// readStdinTrimmed reads all of r and returns the result with surrounding
// whitespace stripped. Empty result is returned as-is for the caller to
// validate.
func readStdinTrimmed(r io.Reader) (string, error) {
	if r == nil {
		return "", nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
