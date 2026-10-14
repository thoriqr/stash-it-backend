package user

import "github.com/jackc/pgx/v5/pgtype"

// mapUserResponse builds the API shape from the service's projection.
//
// It is a separate type from UserProfile rather than a marshalling tag set on it,
// because the two answer different questions: the projection is what the service
// reasons about, and the response is what a client is shown. It is also where the
// identities slice is guaranteed to be non-nil, which is a property of the wire
// shape and not of the service's own result.
func mapUserResponse(
	profile UserProfile,
	identities []UserIdentity,
) UserResponse {
	// An empty slice, never nil. A slice built with make is non-nil even at length
	// zero, and a nil one would serialize as null — a different answer to "this
	// account has no linked providers" than [] is, and one that would make a
	// client check for the key before it could read a length.
	response := make([]UserIdentityResponse, 0, len(identities))

	for _, identity := range identities {
		response = append(response, UserIdentityResponse{
			Provider:            identity.Provider,
			LinkedAt:            identity.LinkedAt,
			EmailSnapshot:       nullableText(identity.EmailSnapshot),
			DisplayNameSnapshot: nullableText(identity.DisplayNameSnapshot),
		})
	}

	return UserResponse{
		ID:                    profile.ID,
		Email:                 profile.Email,
		DisplayName:           profile.DisplayName,
		HasPasswordCredential: profile.HasPasswordCredential,
		CreatedAt:             profile.CreatedAt,
		Identities:            response,
	}
}

// nullableText converts a nullable text column into a pointer for the response.
//
// Valid is the whole test. A column that is NULL produces a nil pointer, which
// marshals as JSON null; a column holding the empty string produces a pointer to
// "", which marshals as an empty JSON string. Reading String alone would collapse
// the two, and reporting "the provider asserted nothing" as "the provider asserted
// an empty value" is a claim the database does not support.
func nullableText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}

	return &value.String
}
