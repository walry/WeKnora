package kb

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

// kbListFields enumerates the fields surfaced for `--format json` discovery on
// `kb list`. Nested config structs (chunking / image / FAQ / VLM / storage
// / extract) are intentionally omitted - users wanting those can use `--jq`
// against the full object. The last three appear only in the shared-space
// views (--space / --shared).
var kbListFields = []string{
	"id", "name", "type", "description",
	"is_temporary", "is_pinned",
	"embedding_model_id", "summary_model_id",
	"knowledge_count", "chunk_count",
	"is_processing", "processing_count",
	"created_at", "updated_at",
	"source", "org_name", "permission",
}

// ListOptions captures `kb list` filter flag state.
type ListOptions struct {
	Pinned bool // --pinned: client-side filter to KBs with IsPinned == true
	// Shared switches to the shared-space view: KBs from every space the
	// active workspace belongs to, own shares included (source=mine).
	Shared bool
	// Space selects the shared-space view for one space (name or UUID).
	// Mutually exclusive with --shared.
	Space string
	// Limit caps the returned slice client-side. 0 = no cap, 1..10000 = explicit.
	// The KB list SDK is unpaginated; --all-pages is intentionally not exposed
	// because it would be a no-op.
	Limit int
}

// ListService is the narrow SDK surface this command depends on.
type ListService interface {
	ListKnowledgeBases(ctx context.Context) ([]sdk.KnowledgeBase, error)
}

// sharedListService is the extra SDK surface the shared-space views need. Kept
// separate from ListService so the own-tenant path (and its fakes) stay
// unchanged.
type sharedListService interface {
	ListMyOrganizations(ctx context.Context) ([]sdk.OrganizationResponse, error)
	ListOrganizationSharedKnowledgeBases(ctx context.Context, orgID string) ([]sdk.SharedKnowledgeBaseInfo, error)
}

// NewCmdList builds `weknora kb list`.
func NewCmdList(f *cmdutil.Factory) *cobra.Command {
	opts := &ListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List knowledge bases visible to the active profile",
		Long: `List knowledge bases visible to the active profile, sorted by most recently updated. Pass --pinned to restrict to pinned KBs.

--space and --shared switch to the shared-space view. Knowledge bases shared
through an organization live in the owner's tenant, so they never appear in the
plain listing and ` + "`--kb <name>`" + ` cannot resolve them — read the id from this view
and pass that id to ` + "`--kb`" + `:

  weknora kb list --space UVDoc --format json --jq '.data[].id'`,
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
			// returns input.invalid_argument (exit 5) rather than an auth error
			// (exit 3) when no profile is configured.
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
	cmd.Flags().BoolVar(&opts.Pinned, "pinned", false, "Only show pinned knowledge bases")
	cmd.Flags().StringVar(&opts.Space, "space", "", "List knowledge bases shared in this space (organization name or UUID)")
	cmd.Flags().BoolVar(&opts.Shared, "shared", false, "List knowledge bases from every space the active workspace belongs to")
	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 30, "Maximum results to return — client-side cap; meta.has_more/total_count report the full size (1..10000)")
	cmdutil.AddFormatFlag(cmd, kbListFields...)
	cmdutil.SetAgentHelp(cmd, cmdutil.AgentHelp{
		UsedFor: "List knowledge bases in the current tenant, or the ones shared through a collaboration space (--space / --shared). --format json emits the standard envelope {ok, data:[...], meta:{count}, profile}.",
		Examples: []string{
			"weknora kb list --format json",
			"weknora kb list --space UVDoc --format json",
			"weknora kb list --shared --format json --jq '.data[] | select(.source==\"shared\") | .id'",
		},
		Output: "envelope.data is an array of KnowledgeBase objects with id, name, is_pinned, type, embedding_model_id; meta.total_count is the full tenant set and meta.has_more=true means --limit truncated it (raise --limit to get the rest). In the --space/--shared view each row is flattened to {id, name, source, org_name, organization_id, permission, source_tenant_id, ...} where id is directly usable as --kb.",
	})
	return cmd
}

// validateListOpts checks --limit. Called from RunE before the client is built
// (so a bad value surfaces as exit 5, not an auth error) and at runList's top
// for direct callers; idempotent.
func validateListOpts(opts *ListOptions) error {
	if opts.Limit < 1 || opts.Limit > 10000 {
		return &cmdutil.Error{
			Code:    cmdutil.CodeInputInvalidArgument,
			Message: fmt.Sprintf("--limit must be in 1..10000, got %d", opts.Limit),
		}
	}
	return nil
}

func runList(ctx context.Context, opts *ListOptions, fopts *cmdutil.FormatOptions, svc ListService) error {
	if err := validateListOpts(opts); err != nil {
		return err
	}
	items, err := svc.ListKnowledgeBases(ctx)
	if err != nil {
		return cmdutil.WrapHTTP(err, "list knowledge bases")
	}
	if items == nil {
		items = []sdk.KnowledgeBase{} // ensure JSON [] not null
	}
	if opts.Pinned {
		filtered := items[:0]
		for _, kb := range items {
			if kb.IsPinned {
				filtered = append(filtered, kb)
			}
		}
		items = filtered
	}
	// Default sort by updated_at desc. Server return order is not
	// guaranteed, so client-side sort makes output deterministic regardless
	// of backend storage choices.
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	// The KB list SDK is unpaginated — it returns every KB in one call —
	// so the CLI holds the true total and can tell the caller whether the
	// client-side --limit dropped any. total_count is the full count;
	// has_more flags that --limit truncated it (raise --limit to get the
	// rest — there is no server cursor to continue with).
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
		if opts.Pinned {
			fmt.Fprintln(iostreams.IO.Out, "(no pinned knowledge bases)")
			return nil
		}
		fmt.Fprintln(iostreams.IO.Out, "(no knowledge bases)")
		return nil
	}

	tw := tabwriter.NewWriter(iostreams.IO.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tDOCS\tUPDATED")
	now := time.Now()
	for _, kb := range items {
		name := text.Truncate(40, kb.Name)
		docs := text.Pluralize(int(kb.KnowledgeCount), "doc")
		updated := text.FuzzyAgo(now, kb.UpdatedAt)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", kb.ID, name, docs, updated)
	}
	return tw.Flush()
}

// sharedKBRow is the flattened projection of a shared knowledge base.
// Flattened rather than nested so `id` sits at the top level exactly like the
// own-tenant rows: an agent can pipe .data[].id straight into --kb without
// knowing about the {knowledge_base:{...}} wrapper the server uses.
type sharedKBRow struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Source         string    `json:"source"` // "shared" (another workspace) | "mine"
	OrgName        string    `json:"org_name"`
	OrganizationID string    `json:"organization_id"`
	Permission     string    `json:"permission"`
	SourceTenantID uint64    `json:"source_tenant_id"`
	KnowledgeCount int64     `json:"knowledge_count"`
	ChunkCount     int64     `json:"chunk_count"`
	IsPinned       bool      `json:"is_pinned"`
	SharedAt       time.Time `json:"shared_at"`
}

// runListShared implements --space / --shared. KBs visible through a space are
// enumerated per space: the server's flat /shared-knowledge-bases endpoint
// omits own shares and embeds no KB object, so it cannot yield an addressable
// id. Non-member spaces are resolved away first, so a 403 here means the
// credential is not allowed to read share metadata at all (scoped API keys
// without manage_spaces) — surfaced, never silently downgraded to an empty
// list, which an agent would read as "the space has no knowledge".
func runListShared(ctx context.Context, opts *ListOptions, fopts *cmdutil.FormatOptions, svc sharedListService) error {
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

	rows := make([]sharedKBRow, 0, len(spaces))
	for _, space := range spaces {
		shares, err := svc.ListOrganizationSharedKnowledgeBases(ctx, space.ID)
		if err != nil {
			return cmdutil.WrapHTTP(err, "list knowledge bases in space %s", space.Name)
		}
		for _, share := range shares {
			// The space endpoint always embeds the KB; a nil row carries no
			// addressable id, so there is nothing useful to print.
			if share.KnowledgeBase == nil {
				continue
			}
			if opts.Pinned && !share.KnowledgeBase.IsPinned {
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
			rows = append(rows, sharedKBRow{
				ID:             share.KnowledgeBase.ID,
				Name:           share.KnowledgeBase.Name,
				Description:    share.KnowledgeBase.Description,
				Source:         source,
				OrgName:        orgName,
				OrganizationID: space.ID,
				Permission:     share.Permission,
				SourceTenantID: share.SourceTenantID,
				KnowledgeCount: share.KnowledgeBase.KnowledgeCount,
				ChunkCount:     share.KnowledgeBase.ChunkCount,
				IsPinned:       share.KnowledgeBase.IsPinned,
				SharedAt:       share.SharedAt,
			})
		}
	}

	// Stable order (space, then name) so the agent's pick is reproducible
	// across runs — server return order is not guaranteed.
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
			fmt.Fprintf(iostreams.IO.Out, "(no knowledge bases shared in space %s)\n", opts.Space)
			return nil
		}
		fmt.Fprintln(iostreams.IO.Out, "(no knowledge bases in any space)")
		return nil
	}
	tw := tabwriter.NewWriter(iostreams.IO.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tSPACE\tPERM\tDOCS")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.ID, text.Truncate(40, row.Name), row.Source,
			text.Truncate(20, row.OrgName), row.Permission,
			text.Pluralize(int(row.KnowledgeCount), "doc"))
	}
	return tw.Flush()
}
