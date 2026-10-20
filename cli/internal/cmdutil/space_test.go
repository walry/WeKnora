package cmdutil

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/Tencent/WeKnora/client"
)

func spaceList() []sdk.OrganizationResponse {
	return []sdk.OrganizationResponse{
		{ID: "11111111-1111-1111-1111-111111111111", Name: "UVDoc"},
		{ID: "22222222-2222-2222-2222-222222222222", Name: "UVDoc Docs"},
		{ID: "33333333-3333-3333-3333-333333333333", Name: "Finance"},
	}
}

func TestMatchSpace_ExactBeatsPrefix(t *testing.T) {
	// "UVDoc" is both an exact name and a prefix of "UVDoc Docs"; the exact
	// hit must win instead of the pair being reported as ambiguous.
	space, err := MatchSpace(spaceList(), "UVDoc")
	require.NoError(t, err)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", space.ID)
}

func TestMatchSpace_CaseInsensitive(t *testing.T) {
	// The shared-space-first protocol in the skills names a space by hand, so
	// a casing slip must resolve rather than dead-end.
	space, err := MatchSpace(spaceList(), "uvdoc docs")
	require.NoError(t, err)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", space.ID)
}

func TestMatchSpace_UniquePrefix(t *testing.T) {
	space, err := MatchSpace(spaceList(), "fin")
	require.NoError(t, err)
	assert.Equal(t, "33333333-3333-3333-3333-333333333333", space.ID)
}

func TestMatchSpace_UUIDPassthrough(t *testing.T) {
	// Known id: pass through with the listed entry so the caller gets a name.
	space, err := MatchSpace(spaceList(), "11111111-1111-1111-1111-111111111111")
	require.NoError(t, err)
	assert.Equal(t, "UVDoc", space.Name)

	// Unknown id: still pass through — membership is the server's call, and a
	// local "not found" would be misleading when the id is simply not visible
	// to ListMyOrganizations.
	const unknown = "44444444-4444-4444-4444-444444444444"
	space, err = MatchSpace(spaceList(), unknown)
	require.NoError(t, err)
	assert.Equal(t, unknown, space.ID)
}

func TestMatchSpace_AmbiguousPrefix(t *testing.T) {
	// "uv" prefixes both UVDoc and UVDoc Docs, neither exactly. Guessing would
	// answer from the wrong shared space, so this must be an error.
	_, err := MatchSpace(spaceList(), "uv")
	require.Error(t, err)
	var typed *Error
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, CodeSpaceAmbiguous, typed.Code)
	assert.Contains(t, typed.Hint, "space UUID")
	assert.Equal(t, 1, ExitCode(err), "local.* maps to exit 1")
}

func TestMatchSpace_NotFound(t *testing.T) {
	_, err := MatchSpace(spaceList(), "Legal")
	require.Error(t, err)
	var typed *Error
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, CodeSpaceNotFound, typed.Code)
	assert.Equal(t, 1, ExitCode(err), "local.* maps to exit 1")
}
