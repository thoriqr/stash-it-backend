package user

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/mock/gomock"

	"github.com/thoriqr/stash-it-backend/internal/api/user/mocks"
)

var (
	testUserID      = uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ed")
	testSessionID   = uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768eb")
	testOtherUserID = uuid.MustParse("01a0f359-093b-737a-963a-80f7ca6768ec")
)

// testText is a present, non-null pgtype.Text.
//
// It exists rather than a pgtype.Text literal so that the tests which care about
// the NULL case have to say NULL explicitly instead of leaving a field out and
// inheriting the zero value by accident.
func testText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

func newServiceTestFixture(t *testing.T) (
	Service,
	*mocks.MockRepository,
	*mocks.MockPasswordHasher,
) {
	t.Helper()

	ctrl := gomock.NewController(t)

	repository := mocks.NewMockRepository(ctrl)
	passwordHasher := mocks.NewMockPasswordHasher(ctrl)

	return NewService(repository, passwordHasher), repository, passwordHasher
}
