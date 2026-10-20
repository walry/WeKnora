package kb

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/cli/internal/cmdutil"
	"github.com/Tencent/WeKnora/cli/internal/iostreams"
	sdk "github.com/Tencent/WeKnora/client"
)

// fakeSharedSvc serves the space-scoped SDK surface `kb list --space/--shared`
// depends on. Spaces and their shares are keyed by org id so the per-space walk
// is observable.
type fakeSharedSvc struct {
	spaces    []sdk.OrganizationResponse
	shares    map[string][]sdk.SharedKnowledgeBaseInfo
	spacesErr error
	sharesErr error
}

func (f *fakeSharedSvc) ListMyOrganizations(context.Context) ([]sdk.OrganizationResponse, error) {
	return f.spaces, f.spacesErr
}

func (f *fakeSharedSvc) ListOrganizationSharedKnowledgeBases(
	_ context.Context, orgID string,
) ([]sdk.SharedKnowledgeBaseInfo, error) {
	if f.sharesErr != nil {
		return nil, f.sharesErr
	}
	return f.shares[orgID], nil
}

func sharedFixture() *fakeSharedSvc {
	return &fakeSharedSvc{
		spaces: []sdk.OrganizationResponse{
			{ID: "org-uvdoc", Name: "UVDoc"},
			{ID: "org-finance", Name: "Finance"},
		},
		shares: map[string][]sdk.SharedKnowledgeBaseInfo{
			"org-uvdoc": {{
				KnowledgeBase: &sdk.KnowledgeBase{
					ID: "kb-uvdoc-1", Name: "UVDoc 产品手册", KnowledgeCount: 12, ChunkCount: 340,
				},
				OrgName:    "UVDoc",
				Permission: "viewer",
			}},
			"org-finance": {{
				KnowledgeBase: &sdk.KnowledgeBase{ID: "kb-fin-1", Name: "财务制度"},
				OrgName:       "Finance",
				Permission:    "viewer",
			}, {
				KnowledgeBase: &sdk.KnowledgeBase{ID: "kb-fin-own", Name: "本租户共享出去的"},
				OrgName:       "Finance",
				Permission:    "editor",
				IsMine:        true,
			}},
		},
	}
}

func decodeSharedRows(t *testing.T, raw string) []sharedKBRow {
	t.Helper()
	var env struct {
		OK   bool          `json:"ok"`
		Data []sharedKBRow `json:"data"`
		Meta *struct {
			Count      int  `json:"count"`
			TotalCount int  `json:"total_count"`
			HasMore    bool `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode envelope: %v (raw=%q)", err, raw)
	}
	if !env.OK {
		t.Fatalf("ok=false in envelope: %q", raw)
	}
	return env.Data
}

func TestListShared_SpaceView_JSON(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	opts := &ListOptions{Space: "uvdoc", Limit: 30}
	fopts := &cmdutil.FormatOptions{Mode: cmdutil.FormatJSON}
	if err := runListShared(context.Background(), opts, fopts, sharedFixture()); err != nil {
		t.Fatalf("runListShared: %v", err)
	}

	rows := decodeSharedRows(t, out.String())
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d (%q)", len(rows), out.String())
	}
	// The id must be the KB uuid, not the share id or the org id: this is the
	// value an agent pipes into --kb.
	if rows[0].ID != "kb-uvdoc-1" {
		t.Errorf("id = %q, want kb-uvdoc-1", rows[0].ID)
	}
	if rows[0].Source != "shared" {
		t.Errorf("source = %q, want shared", rows[0].Source)
	}
	if rows[0].Permission != "viewer" {
		t.Errorf("permission = %q, want viewer", rows[0].Permission)
	}
	if rows[0].OrgName != "UVDoc" {
		t.Errorf("org_name = %q, want UVDoc", rows[0].OrgName)
	}
}

func TestListShared_AllSpaces_MarksOwnShares(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	opts := &ListOptions{Shared: true, Limit: 30}
	fopts := &cmdutil.FormatOptions{Mode: cmdutil.FormatJSON}
	if err := runListShared(context.Background(), opts, fopts, sharedFixture()); err != nil {
		t.Fatalf("runListShared: %v", err)
	}

	rows := decodeSharedRows(t, out.String())
	if len(rows) != 3 {
		t.Fatalf("want 3 rows across both spaces, got %d", len(rows))
	}
	sources := map[string]string{}
	for _, row := range rows {
		sources[row.ID] = row.Source
	}
	if sources["kb-fin-own"] != "mine" {
		t.Errorf("own share source = %q, want mine", sources["kb-fin-own"])
	}
	if sources["kb-uvdoc-1"] != "shared" {
		t.Errorf("foreign share source = %q, want shared", sources["kb-uvdoc-1"])
	}
}

func TestListShared_SpaceView_Text(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	opts := &ListOptions{Space: "UVDoc", Limit: 30}
	if err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, sharedFixture()); err != nil {
		t.Fatalf("runListShared: %v", err)
	}
	text := out.String()
	for _, want := range []string{"ID", "NAME", "SOURCE", "SPACE", "kb-uvdoc-1", "viewer"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestListShared_UnknownSpace_IsLocalError(t *testing.T) {
	_, _ = iostreams.SetForTest(t)
	opts := &ListOptions{Space: "Legal", Limit: 30}
	err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, sharedFixture())
	if err == nil {
		t.Fatal("expected an error for an unknown space")
	}
	var typed *cmdutil.Error
	if !errors.As(err, &typed) || typed.Code != cmdutil.CodeSpaceNotFound {
		t.Fatalf("want local.space_not_found, got %v", err)
	}
	if got := cmdutil.ExitCode(err); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

// A 403 on the space listing must surface, not degrade into an empty list: an
// empty list reads as "the space has no knowledge", which would send an agent
// back to the own-tenant corpus while claiming the shared space was checked.
func TestListShared_Forbidden_IsSurfaced(t *testing.T) {
	_, _ = iostreams.SetForTest(t)
	svc := sharedFixture()
	svc.sharesErr = errors.New("403 Forbidden")
	opts := &ListOptions{Space: "UVDoc", Limit: 30}
	err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatJSON}, svc)
	if err == nil {
		t.Fatal("403 must not be swallowed into an empty list")
	}
}

func TestListShared_EmptySpace_Text(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	svc := sharedFixture()
	svc.shares = map[string][]sdk.SharedKnowledgeBaseInfo{}
	opts := &ListOptions{Space: "UVDoc", Limit: 30}
	if err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, svc); err != nil {
		t.Fatalf("runListShared: %v", err)
	}
	if !strings.Contains(out.String(), "no knowledge bases shared in space UVDoc") {
		t.Errorf("unexpected empty message: %q", out.String())
	}
}
