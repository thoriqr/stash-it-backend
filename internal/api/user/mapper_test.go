package user

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// The wire contract for the two snapshot fields is that a NULL column is JSON
// null and an asserted empty string is JSON "". They are different facts — a
// provider that said nothing is not a provider that said nothing at all — and a
// response that collapses them tells a client something the database does not
// support.
//
// Every case is asserted against the marshalled bytes rather than the Go value,
// because the question being asked is what a client decodes.
func TestMapUserResponse_SnapshotsPreserveNullabilityOnTheWire(t *testing.T) {
	linkedAt := time.Date(2026, 10, 1, 10, 30, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		identity UserIdentity
		expect   string
	}{
		{
			name: "both present",
			identity: UserIdentity{
				Provider:            "google",
				LinkedAt:            linkedAt,
				EmailSnapshot:       testText("present@example.com"),
				DisplayNameSnapshot: testText("Present Name"),
			},
			expect: `{"provider":"google","linked_at":"2026-10-01T10:30:00Z",` +
				`"email_snapshot":"present@example.com",` +
				`"display_name_snapshot":"Present Name"}`,
		},
		{
			name: "both null",
			identity: UserIdentity{
				Provider:            "sparse-provider",
				LinkedAt:            linkedAt,
				EmailSnapshot:       pgtype.Text{},
				DisplayNameSnapshot: pgtype.Text{},
			},
			expect: `{"provider":"sparse-provider","linked_at":"2026-10-01T10:30:00Z",` +
				`"email_snapshot":null,"display_name_snapshot":null}`,
		},
		{
			name: "email null, display name present",
			identity: UserIdentity{
				Provider:            "half-provider",
				LinkedAt:            linkedAt,
				EmailSnapshot:       pgtype.Text{},
				DisplayNameSnapshot: testText("Only A Name"),
			},
			expect: `{"provider":"half-provider","linked_at":"2026-10-01T10:30:00Z",` +
				`"email_snapshot":null,"display_name_snapshot":"Only A Name"}`,
		},
		{
			name: "email present, display name null",
			identity: UserIdentity{
				Provider:            "half-provider",
				LinkedAt:            linkedAt,
				EmailSnapshot:       testText("only@example.com"),
				DisplayNameSnapshot: pgtype.Text{},
			},
			expect: `{"provider":"half-provider","linked_at":"2026-10-01T10:30:00Z",` +
				`"email_snapshot":"only@example.com","display_name_snapshot":null}`,
		},
		{
			name: "both asserted empty is not null",
			identity: UserIdentity{
				Provider:            "blank-provider",
				LinkedAt:            linkedAt,
				EmailSnapshot:       testText(""),
				DisplayNameSnapshot: testText(""),
			},
			expect: `{"provider":"blank-provider","linked_at":"2026-10-01T10:30:00Z",` +
				`"email_snapshot":"","display_name_snapshot":""}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := mapUserResponse(
				UserProfile{ID: testUserID},
				[]UserIdentity{tc.identity},
			)

			encoded, err := json.Marshal(response.Identities[0])
			require.NoError(t, err)
			require.JSONEq(t, tc.expect, string(encoded))
		})
	}
}

// Every identity key is present whether or not its value is null. A client
// reading one field has to be able to do it without first checking whether the
// key arrived, which is the same convention the collections listing follows.
func TestMapUserResponse_IdentityKeysAreAlwaysPresent(t *testing.T) {
	response := mapUserResponse(
		UserProfile{ID: testUserID},
		[]UserIdentity{
			{Provider: "sparse-provider"},
		},
	)

	encoded, err := json.Marshal(response.Identities[0])
	require.NoError(t, err)

	var decoded map[string]any

	require.NoError(t, json.Unmarshal(encoded, &decoded))

	require.ElementsMatch(
		t,
		[]string{"provider", "linked_at", "email_snapshot", "display_name_snapshot"},
		keysOf(decoded),
	)

	// Present with a null value, not absent.
	require.Contains(t, decoded, "email_snapshot")
	require.Nil(t, decoded["email_snapshot"])
	require.Contains(t, decoded, "display_name_snapshot")
	require.Nil(t, decoded["display_name_snapshot"])
}

// An account with nothing linked still reports an empty array, never null. The
// expansion of the identity object must not have changed that.
func TestMapUserResponse_NoIdentitiesIsStillAnEmptyArray(t *testing.T) {
	response := mapUserResponse(UserProfile{ID: testUserID}, nil)

	encoded, err := json.Marshal(response.Identities)
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(encoded))
}

func keysOf(raw map[string]any) []string {
	keys := make([]string, 0, len(raw))

	for key := range raw {
		keys = append(keys, key)
	}

	return keys
}
