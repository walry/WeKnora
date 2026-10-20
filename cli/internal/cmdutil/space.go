package cmdutil

import (
	"fmt"
	"strings"

	sdk "github.com/Tencent/WeKnora/client"
)

// MatchSpace picks one space (organization) out of an already-fetched list.
// `kb list --space` / `agent list --space` enumerate every space anyway, so they
// resolve the selector against that list rather than issuing a second
// ListMyOrganizations round-trip.
//
// A UUID passes through untouched (membership is the server's call — a local
// "not found" would be misleading when the id simply is not in the caller's
// list). Anything else is matched:
//
//  1. case-insensitive exact name ("uvdoc" → "UVDoc");
//  2. otherwise a unique case-insensitive prefix ("uvdoc" → "UVDoc Docs");
//  3. several matches is an error, never a guess — answering from the wrong
//     shared space is worse than making the caller retry.
//
// Deliberately looser than ResolveKBNameToID (exact, case-sensitive): space
// names are free-form display strings and the shared-space-first protocol in
// the bundled skills names a space by hand, so a casing slip should resolve
// rather than dead-end.
func MatchSpace(spaces []sdk.OrganizationResponse, selector string) (sdk.OrganizationResponse, error) {
	trimmed := strings.TrimSpace(selector)
	if uuidPattern.MatchString(trimmed) {
		for _, space := range spaces {
			if space.ID == trimmed {
				return space, nil
			}
		}
		return sdk.OrganizationResponse{ID: trimmed}, nil
	}

	needle := strings.ToLower(trimmed)
	var exact, prefix []sdk.OrganizationResponse
	for _, space := range spaces {
		name := strings.ToLower(strings.TrimSpace(space.Name))
		switch {
		case name == needle:
			exact = append(exact, space)
		case strings.HasPrefix(name, needle):
			prefix = append(prefix, space)
		}
	}

	matches := exact
	if len(matches) == 0 {
		matches = prefix
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return sdk.OrganizationResponse{}, NewError(CodeSpaceNotFound,
			fmt.Sprintf("space not found: %s", trimmed)).
			WithHint("the active workspace is not a member of that space, or the name is misspelled; " +
				"list spaces with `weknora api /api/v1/organizations`")
	default:
		return sdk.OrganizationResponse{}, NewError(CodeSpaceAmbiguous,
			fmt.Sprintf("space %q is ambiguous: %s", trimmed, spaceNames(matches))).
			WithHint("re-run with the space UUID or its full name")
	}
}

func spaceNames(spaces []sdk.OrganizationResponse) string {
	names := make([]string, 0, len(spaces))
	for _, space := range spaces {
		names = append(names, fmt.Sprintf("%s (%s)", space.Name, space.ID))
	}
	return strings.Join(names, ", ")
}
