package login

type LoginOutcome string

const (
	LoginOutcomeAuthenticated        LoginOutcome = "authenticated"
	LoginOutcomeAccountLinkRequired  LoginOutcome = "account_link_required"
	LoginOutcomeRegistrationRequired LoginOutcome = "registration_required"
)
