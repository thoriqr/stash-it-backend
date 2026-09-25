package login

func mapLoginManualResponse(result LoginResult) LoginManualResponse {
	return LoginManualResponse{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		User: LoginUser{
			ID:          result.User.ID.String(),
			Email:       result.User.Email,
			DisplayName: result.User.DisplayName,
		},
	}
}

func mapLoginGoogleResponse(result LoginGoogleResult) LoginGoogleResponse {
	response := LoginGoogleResponse{
		Outcome: result.Outcome,
	}

	switch result.Outcome {
	case LoginOutcomeAuthenticated:
		response.AccessToken = result.AccessToken
		response.RefreshToken = result.RefreshToken

		if result.User != nil {
			response.User = &LoginUser{
				ID:          result.User.ID.String(),
				Email:       result.User.Email,
				DisplayName: result.User.DisplayName,
			}
		}

	case LoginOutcomeAccountLinkRequired:
		response.Provider = "google"
		response.ConfirmationID = result.ConfirmationID.String()

	case LoginOutcomeRegistrationRequired:
		response.VerificationID = result.VerificationID.String()
	}

	return response
}