package registration

import (
	"math"
	"time"
)

func mapGetVerificationResponse(
	result GetVerificationResult,
) GetVerificationResponse {
	resendInSeconds := 0
	pinIssued := result.PinIssuedCount > 0

	if result.LastSentAt != nil {
		resendAt := result.LastSentAt.Add(
			verificationResendCooldown,
		)

		remaining := time.Until(resendAt)

		if remaining > 0 {
			resendInSeconds = int(math.Ceil(remaining.Seconds()))
		}
	}

	return GetVerificationResponse{
		VerificationID:  result.VerificationID.String(),
		Status:          string(result.Status),
		PINIssued:       pinIssued,
		ResendInSeconds: resendInSeconds,
	}
}

func mapFinalizeSocialRegistrationResponse(
	result FinalizeSocialRegistrationResult,
) FinalizeSocialRegistrationResponse {
	return FinalizeSocialRegistrationResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		User: FinalizeSocialRegistrationUser{
			ID:          result.UserID.String(),
			Email:       result.Email,
			DisplayName: result.DisplayName,
		},
	}
}