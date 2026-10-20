package agentcmd

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

// fakeSharedAgentSvc serves the space-scoped SDK surface `agent list
// --space/--shared` depends on.
type fakeSharedAgentSvc struct {
	spaces    []sdk.OrganizationResponse
	shares    map[string][]sdk.SharedAgentInfo
	spacesErr error
	sharesErr error
}

func (f *fakeSharedAgentSvc) ListMyOrganizations(context.Context) ([]sdk.OrganizationResponse, error) {
	return f.spaces, f.spacesErr
}

func (f *fakeSharedAgentSvc) ListOrganizationSharedAgents(
	_ context.Context, orgID string,
) ([]sdk.SharedAgentInfo, error) {
	if f.sharesErr != nil {
		return nil, f.sharesErr
	}
	return f.shares[orgID], nil
}

func sharedAgentFixture() *fakeSharedAgentSvc {
	return &fakeSharedAgentSvc{
		spaces: []sdk.OrganizationResponse{{ID: "org-uvdoc", Name: "UVDoc"}},
		shares: map[string][]sdk.SharedAgentInfo{
			"org-uvdoc": {{
				Agent:      &sdk.Agent{ID: "ag-uvdoc", Name: "UVDoc 问答助手"},
				OrgName:    "UVDoc",
				Permission: "viewer",
			}, {
				Agent:      &sdk.Agent{ID: "ag-own", Name: "本租户共享出去的"},
				OrgName:    "UVDoc",
				Permission: "editor",
				IsMine:     true,
			}},
		},
	}
}

func decodeSharedAgentRows(t *testing.T, raw string) []sharedAgentRow {
	t.Helper()
	var env struct {
		OK   bool             `json:"ok"`
		Data []sharedAgentRow `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode envelope: %v (raw=%q)", err, raw)
	}
	if !env.OK {
		t.Fatalf("ok=false in envelope: %q", raw)
	}
	return env.Data
}

func TestAgentListShared_SpaceView_JSON(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	opts := &ListOptions{Space: "uvdoc", Limit: 30}
	fopts := &cmdutil.FormatOptions{Mode: cmdutil.FormatJSON}
	if err := runListShared(context.Background(), opts, fopts, sharedAgentFixture()); err != nil {
		t.Fatalf("runListShared: %v", err)
	}

	rows := decodeSharedAgentRows(t, out.String())
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d (%q)", len(rows), out.String())
	}
	// id must be the agent id, the value `session ask --agent` takes.
	if rows[0].ID != "ag-own" && rows[1].ID != "ag-own" {
		t.Errorf("expected the own share to be listed, got %+v", rows)
	}
	byID := map[string]sharedAgentRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if byID["ag-uvdoc"].Source != "shared" {
		t.Errorf("foreign agent source = %q, want shared", byID["ag-uvdoc"].Source)
	}
	if byID["ag-own"].Source != "mine" {
		t.Errorf("own agent source = %q, want mine", byID["ag-own"].Source)
	}
}

func TestAgentListShared_Text(t *testing.T) {
	out, _ := iostreams.SetForTest(t)
	opts := &ListOptions{Space: "UVDoc", Limit: 30}
	if err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, sharedAgentFixture()); err != nil {
		t.Fatalf("runListShared: %v", err)
	}
	text := out.String()
	for _, want := range []string{"SOURCE", "SPACE", "ag-uvdoc", "UVDoc 问答助手"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentListShared_UnknownSpace_IsLocalError(t *testing.T) {
	_, _ = iostreams.SetForTest(t)
	opts := &ListOptions{Space: "Legal", Limit: 30}
	err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatText}, sharedAgentFixture())
	if err == nil {
		t.Fatal("expected an error for an unknown space")
	}
	var typed *cmdutil.Error
	if !errors.As(err, &typed) || typed.Code != cmdutil.CodeSpaceNotFound {
		t.Fatalf("want local.space_not_found, got %v", err)
	}
}

// 403 on the space listing must surface rather than look like "no shared agent
// here" — the latter would push the agent back to its own tenant silently.
func TestAgentListShared_Forbidden_IsSurfaced(t *testing.T) {
	_, _ = iostreams.SetForTest(t)
	svc := sharedAgentFixture()
	svc.sharesErr = errors.New("403 Forbidden")
	opts := &ListOptions{Space: "UVDoc", Limit: 30}
	if err := runListShared(context.Background(), opts, &cmdutil.FormatOptions{Mode: cmdutil.FormatJSON}, svc); err == nil {
		t.Fatal("403 must not be swallowed into an empty list")
	}
}
