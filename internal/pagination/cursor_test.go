package pagination_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/thoriqr/stash-it-backend/internal/pagination"
)

// payload is a stand-in for a feature cursor. It carries pointer fields for the
// same reason the real ones do: absence has to stay distinguishable from zero.
type payload struct {
	Version int        `json:"v"`
	Group   *int       `json:"g"`
	Value   *string    `json:"val"`
	ID      *uuid.UUID `json:"i"`
}

func (p payload) CursorVersion() int {
	return p.Version
}

func TestCursor_RoundTrip(t *testing.T) {
	t.Run("preserves every field", func(t *testing.T) {
		group := 1
		value := "some-position"
		id := uuid.New()

		encoded, err := pagination.Encode(payload{
			Version: pagination.CurrentVersion,
			Group:   &group,
			Value:   &value,
			ID:      &id,
		})
		require.NoError(t, err)

		decoded, err := pagination.Decode[payload](encoded)
		require.NoError(t, err)

		require.Equal(t, pagination.CurrentVersion, decoded.Version)
		require.NotNil(t, decoded.Group)
		require.Equal(t, group, *decoded.Group)
		require.NotNil(t, decoded.Value)
		require.Equal(t, value, *decoded.Value)
		require.NotNil(t, decoded.ID)
		require.Equal(t, id, *decoded.ID)
	})

	// A token is a query parameter, so it has to travel without escaping. RawURL
	// encoding is what makes that true, and a token containing + or / would be
	// rewritten in transit.
	t.Run("produces a url safe token", func(t *testing.T) {
		encoded, err := pagination.Encode(map[string]string{
			"value": "a+b/c=d?e&f",
		})
		require.NoError(t, err)

		require.NotContains(t, encoded, "+")
		require.NotContains(t, encoded, "/")
		require.NotContains(t, encoded, "=")
	})

	// This is the property the whole envelope exists for. Decoding a payload that
	// omits Group must leave it nil, not 0, because a feature reads that difference as
	// "no group given" rather than "the zero group".
	t.Run("keeps an absent field distinct from a zero value", func(t *testing.T) {
		encoded, err := pagination.Encode(map[string]int{"v": 1})
		require.NoError(t, err)

		decoded, err := pagination.Decode[payload](encoded)
		require.NoError(t, err)

		require.Nil(t, decoded.Group)
		require.Nil(t, decoded.Value)
		require.Nil(t, decoded.ID)

		zero := 0

		encoded, err = pagination.Encode(payload{Version: 1, Group: &zero})
		require.NoError(t, err)

		decoded, err = pagination.Decode[payload](encoded)
		require.NoError(t, err)

		require.NotNil(t, decoded.Group)
		require.Equal(t, 0, *decoded.Group)
	})
}

func TestCursor_DecodeRejectsMalformed(t *testing.T) {
	t.Run("rejects a token that is not base64", func(t *testing.T) {
		_, err := pagination.Decode[payload]("not a token!!!")

		require.ErrorIs(t, err, pagination.ErrMalformed)
	})

	t.Run("rejects base64 that is not json", func(t *testing.T) {
		token := base64.RawURLEncoding.EncodeToString([]byte("not json"))

		_, err := pagination.Decode[payload](token)

		require.ErrorIs(t, err, pagination.ErrMalformed)
	})

	// A payload of the wrong shape is malformed rather than a version problem: the
	// token cannot be read at all, so there is no version to disagree about.
	t.Run("rejects json of the wrong shape", func(t *testing.T) {
		token := base64.RawURLEncoding.EncodeToString([]byte(`["array"]`))

		_, err := pagination.Decode[payload](token)

		require.ErrorIs(t, err, pagination.ErrMalformed)
	})

	t.Run("rejects an empty token", func(t *testing.T) {
		_, err := pagination.Decode[payload]("")

		require.ErrorIs(t, err, pagination.ErrMalformed)
	})
}

func TestCursor_DecodeVersioned(t *testing.T) {
	t.Run("accepts the current version", func(t *testing.T) {
		group := 0

		token, err := pagination.Encode(payload{
			Version: pagination.CurrentVersion,
			Group:   &group,
		})
		require.NoError(t, err)

		decoded, err := pagination.DecodeVersioned[payload](token)
		require.NoError(t, err)

		require.Equal(t, pagination.CurrentVersion, decoded.Version)
	})

	// A cursor can outlive a deployment, so a token written by another build has to
	// fail cleanly rather than be read as a position it does not describe.
	t.Run("rejects a different version", func(t *testing.T) {
		group := 1

		token, err := pagination.Encode(payload{
			Version: pagination.CurrentVersion + 1,
			Group:   &group,
		})
		require.NoError(t, err)

		_, err = pagination.DecodeVersioned[payload](token)

		require.ErrorIs(t, err, pagination.ErrUnsupportedVersion)
	})

	t.Run("reports a malformed token as malformed, not as a version", func(t *testing.T) {
		_, err := pagination.DecodeVersioned[payload]("!!!")

		require.ErrorIs(t, err, pagination.ErrMalformed)
		require.NotErrorIs(t, err, pagination.ErrUnsupportedVersion)
	})
}

func TestCursor_EncodeRejectsUnmarshalable(t *testing.T) {
	// Swallowing this would hand back a token that decodes to nothing, which would
	// surface later as a cursor error against a caller who did nothing wrong.
	_, err := pagination.Encode(make(chan int))

	require.Error(t, err)

	var typeErr *json.UnsupportedTypeError

	require.True(
		t,
		errors.As(err, &typeErr),
		"the underlying cause must survive for the log",
	)
}
