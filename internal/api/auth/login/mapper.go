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