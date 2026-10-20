package agentcmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tencent/WeKnora/cli/internal/cmdutil"
	"github.com/Tencent/WeKnora/cli/internal/iostreams"
	"github.com/Tencent/WeKnora/cli/internal/output"
	"github.com/Tencent/WeKnora/cli/internal/text"
	sdk "github.com/Tencent/WeKnora/client"
)

// agentListFields enumerates the fields surfaced for `--format json` discovery
// on `agent list`. Mirrors the json tags on sdk.Agent - nested Config is
// omitted because its sub-fields make filtering noisy (use `--jq` instead).
var agentListFields = []string{
	"id", "name", "description", "avatar",
	"is_builtin", "tenant_id", "created_by",
	"created_at", "updated_at",
}

// ListService is the narrow SDK surface this command depends on.
type ListService interface {
	ListAgents(ctx context.Context) ([]sdk.Agent, error)
}

// ListOptions captures `agent list` filter flag state.
type ListOptions struct {
	// Limit caps the returned slice client-side. 0 = no cap, 1..10000 = explicit.
	// The agent list SDK is unpaginated; --all-pages is intentionally not
	// exposed because it would be a no-op.
	Limit int
	// Shared switches to the shared-space view: agents from every space the
	// active workspace belongs to, own shares included (source=mine).
	Shared bool
	// Space narrows the shared-space view to one space (name or UUID).
	// Mutually exclusive with --shared.
	Space string
}

// sharedListService is the extra SDK surface the shared-space views need. Kept
// separate from ListService so the own-tenant path (and its fakes) stay
// unchanged.
type sharedListService interface {
	ListMyOrganizations(ctx context.Context) ([]sdk.OrganizationResponse, error)
	ListOrganizationSharedAgents(ctx context.Context, orgID string) ([]sdk.SharedAgentInfo, error)
}

// NewCmdList builds `weknora agent list`.
func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	opts := &ListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List custom agents visible to the active tenant",
		Long: `List custom agents visible to the active tenant.

--space and --shared switch to the shared-space view: agents another workspace
shared into a collaboration space are addressable with ` + "`session ask --agent`" + `,
but they never appear in the plain listing:

  weknora agent list --space UVDoc --format json --jq '.data[].id'`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fopts, err := cmdutil.CheckFormatFlag(c)
			if err != nil {
				return err
			}
			fopts.ResolveDefault(iostreams.IO.IsStdoutTTY())
			if opts.Space != "" && opts.Shared {
				return cmdutil.NewFlagError(
					errors.New("--space already selects one space; drop --shared"))
			}
			// Validate static input before building the client so a bad --limit
			// returns input.invalid_argument (exit 5), not an auth error (exit 3).
			if err := validateListOpts(opts); err != nil {
				return err
			}
			cli, err := f.Client()
			if err != nil {
				return err
			}
			if opts.Space != "" || opts.Shared {
				return runListShared(c.Context(), opts, fopts, cli)
			}
			return runList(c.Context(), opts, fopts, cli)
		},
	}
	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 30, "Maximum results to return — client-side cap; meta.has_more/total_count report the full size (1..10000)")
	cmd.Flags().StringVar(&opts.Space, "space", "", "List agents shared in this space (organization name or UUID)")
	cmd.Flags().BoolVar(&opts.Shared, "shared", false, "List agents from every space the active workspace belongs to")
	cmdutil.AddFormatFlag(cmd, agentListFields...)
	cmdutil.SetAgentHelp(cmd, cmdutil.AgentHelp{
		UsedFor: "List custom agents visible to the active tenant, or the ones shared through a collaboration space (--space / --shared). " +
			"The SDK returns all agents in one call (no server-side pagination); meta.count reflects the full set, --limit caps client-side.",
		Examples: []string{
			"weknora agent list --format json",
			"weknora agent list --space UVDoc --format json",
			"weknora agent list --limit 10 --format json",
		},
		Output: "envelope.data is an array of Agent objects with id, name, is_builtin; meta.total_count is the full tenant set and meta.has_more=true means --limit truncated it (raise --limit to get the rest). In the --space/--shared view each row is flattened to {id, name, source, org_name, organization_id, permission, source_tenant_id} where id is directly usable as `session ask --agent`.",
	})
	return cmd
}

// validateListOpts checks --limit. Called from RunE before the client is built
// (so a bad value surfaces as exit 5, not an auth error) and at runList's top
// for direct callers; idempotent and nil-safe.
func validateListOpts(opts *ListOptions) error {
	if opts == nil {
		return nil
	}
	if opts.Limit < 1 || opts.Limit > 10000 {
		return &cmdutil.Error{
			Code:    cmdutil.CodeInputInvalidArgument,
			Message: fmt.Sprintf("--limit must be in 1..10000, got %d", opts.Limit),
		}
	}
	return nil
}

func runList(ctx context.Context, opts *ListOptions, fopts *cmdutil.FormatOptions, svc ListService) error {
	if opts == nil {
		opts = &ListOptions{}
	}
	if err := validateListOpts(opts); err != nil {
		return err
	}
	items, err := svc.ListAgents(ctx)
	if err != nil {
		return cmdutil.WrapHTTP(err, "list agents")
	}
	if items == nil {
		items = []sdk.Agent{} // ensure JSON [] not null
	}
	// Default sort: updated_at desc - most recently-edited agents surface
	// first. Mirrors kb list / doc list behavior.
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	// The agent list SDK is unpaginated — it returns every agent in one
	// call — so the CLI holds the true total and can tell the caller
	// whether the client-side --limit dropped any. total_count is the full
	// count; has_more flags that --limit truncated it (raise --limit to get
	// the rest — there is no server cursor to continue with).
	total := len(items)
	truncated := false
	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
		truncated = true
	}

	if fopts.WantsJSON() {
		meta := &output.Meta{Count: output.IntPtr(len(items)), HasMore: truncated, TotalCount: output.IntPtr(total)}
		return fopts.Emit(iostreams.IO.Out, items, meta)
	}

	if len(items) == 0 {
		fmt.Fprintln(iostreams.IO.Out, "(no agents)")
		return nil
	}

	tw := tabwriter.NewWriter(iostreams.IO.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tBUILTIN\tUPDATED")
	now := time.Now()
	for _, a := range items {
		name := text.Truncate(40, a.Name)
		builtin := "-"
		if a.IsBuiltin {
			builtin = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.ID, name, builtin, text.FuzzyAgo(now, a.UpdatedAt))
	}
	return tw.Flush()
}

// sharedAgentRow is the flattened projection of a shared agent. Flattened so
// `id` sits at the top level and can be piped straight into
// `session ask --agent <id>`, mirroring the own-tenant rows.
type sharedAgentRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	Source         string `json:"source"` // "shared" (another workspace) | "mine"
	OrgName        string `json:"org_name"`
	OrganizationID string `json:"organization_id"`
	Permission     string `json:"permission"`
	SourceTenantID uint64 `json:"source_tenant_id"`
	IsBuiltin      bool   `json:"is_builtin"`
}

// runListShared implements --space / --shared by walking the spaces the active
// workspace belongs to. A non-member space is resolved away first, so a 403
// bubbling up here means the credential may not read share metadata at all
// (scoped API keys without manage_spaces) — surfaced rather than swallowed into
// an empty list, which an agent would read as "this space has no agent".
func runListShared(ctx context.Context, opts *ListOptions, fopts *cmdutil.FormatOptions, svc sharedListService) error {
	if opts == nil {
		opts = &ListOptions{}
	}
	if err := validateListOpts(opts); err != nil {
		return err
	}
	spaces, err := svc.ListMyOrganizations(ctx)
	if err != nil {
		return cmdutil.WrapHTTP(err, "list spaces")
	}
	if opts.Space != "" {
		space, err := cmdutil.MatchSpace(spaces, opts.Space)
		if err != nil {
			return err
		}
		spaces = []sdk.OrganizationResponse{space}
	}

	rows := make([]sharedAgentRow, 0, len(spaces))
	for _, space := range spaces {
		shares, err := svc.ListOrganizationSharedAgents(ctx, space.ID)
		if err != nil {
			return cmdutil.WrapHTTP(err, "list agents in space %s", space.Name)
		}
		for _, share := range shares {
			// A nil Agent row carries no addressable id; nothing to print.
			if share.Agent == nil {
				continue
			}
			source := "shared"
			if share.IsMine {
				source = "mine"
			}
			orgName := share.OrgName
			if orgName == "" {
				orgName = space.Name
			}
			rows = append(rows, sharedAgentRow{
				ID:             share.Agent.ID,
				Name:           share.Agent.Name,
				Description:    share.Agent.Description,
				Source:         source,
				OrgName:        orgName,
				OrganizationID: space.ID,
				Permission:     share.Permission,
				SourceTenantID: share.SourceTenantID,
				IsBuiltin:      share.Agent.IsBuiltin,
			})
		}
	}

	// Stable order (space, then name) so the agent's pick is reproducible.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OrgName != rows[j].OrgName {
			return rows[i].OrgName < rows[j].OrgName
		}
		return rows[i].Name < rows[j].Name
	})
	total := len(rows)
	truncated := false
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
		truncated = true
	}

	if fopts.WantsJSON() {
		meta := &output.Meta{Count: output.IntPtr(len(rows)), HasMore: truncated, TotalCount: output.IntPtr(total)}
		return fopts.Emit(iostreams.IO.Out, rows, meta)
	}
	if len(rows) == 0 {
		if opts.Space != "" {
			fmt.Fprintf(iostreams.IO.Out, "(no agents shared in space %s)\n", opts.Space)
			return nil
		}
		fmt.Fprintln(iostreams.IO.Out, "(no agents in any space)")
		return nil
	}
	tw := tabwriter.NewWriter(iostreams.IO.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tSPACE\tPERM")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.ID, text.Truncate(40, row.Name), row.Source,
			text.Truncate(20, row.OrgName), row.Permission)
	}
	return tw.Flush()
}
