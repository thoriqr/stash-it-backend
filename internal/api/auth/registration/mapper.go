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