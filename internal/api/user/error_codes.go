package user

const (
	// CodeUserNotFound reports that no user holds the authenticated user's id.
	//
	// It is reachable rather than theoretical. Access tokens are stateless and
	// outlive the session they name, so a token minted moments before its account
	// was deleted still verifies and still carries this id. Unknown and deleted
	// are the same answer, which is also what keeps the endpoint from disclosing
	// whether an id exists.
	CodeUserNotFound = "USER_NOT_FOUND"

	// CodeCurrentPasswordInvalid reports that the password submitted alongside a
	// change did not match the stored credential.
	//
	// It is 401 rather than 400 because the credential is what failed, and it is a
	// separate code rather than a generic unauthorized so a client can tell "retry
	// with a different password" apart from "this token is not valid".
	CodeCurrentPasswordInvalid = "CURRENT_PASSWORD_INVALID"

	// CodePasswordCredentialAlreadyExists reports that a concurrent request
	// created the credential first.
	//
	// It is what a plain INSERT loses to, on the credential's primary key. The
	// alternative — an upsert, as the password-reset flow uses — would report
	// success and silently keep whichever password committed last, so two people
	// creating a first password at the same moment would both be told they had.
	CodePasswordCredentialAlreadyExists = "PASSWORD_CREDENTIAL_ALREADY_EXISTS"

	// CodePasswordCredentialNotFound reports that a password change was requested
	// for an account that holds no credential.
	//
	// It is 409 and not 404 because nothing is missing: the account exists and the
	// caller is looking at it. What is missing is the thing the request assumed
	// was there, and the fix is to send the request without a current password.
	CodePasswordCredentialNotFound = "PASSWORD_CREDENTIAL_NOT_FOUND"

	// CodePasswordAlreadyChanged reports that the credential changed after this
	// request read it and verified a password against it.
	//
	// It exists because the alternative is a silent lost update: two requests
	// changing the same password both verify against the same row, and only one
	// can be right. The loser is told its basis expired rather than being told it
	// succeeded while the account moved underneath it.
	CodePasswordAlreadyChanged = "PASSWORD_ALREADY_CHANGED"

	// CodeUnexpectedCurrentPassword reports that a current_password was supplied
	// for an account that holds no credential.
	//
	// It is a 400 rather than being quietly ignored. Ignoring it would report
	// success for a secret that was never checked, which is the same lie a generic
	// invalid-credentials response tells — except here the caller would have no
	// way to tell it had happened at all.
	CodeUnexpectedCurrentPassword = "UNEXPECTED_CURRENT_PASSWORD"

	// CodeCurrentPasswordRequired reports that a password change omitted the
	// current_password the account's existing credential requires.
	//
	// It is a 400 rather than CodeCurrentPasswordInvalid because the two ask for
	// opposite things: this one means add the field and send it again, that one
	// means the password you did send is not the right one. A client that cannot
	// tell them apart either retries forever with the wrong value or gives up on a
	// form it filled in correctly.
	CodeCurrentPasswordRequired = "CURRENT_PASSWORD_REQUIRED"

	// CodePasswordWorkUnavailable reports that no password-work capacity became
	// free before the configured wait ran out.
	//
	// The code and message are the ones login, registration and password reset
	// already return, deliberately. It is the same condition with the same recovery
	// for every caller that performs password work, so a client learns one rule
	// rather than three; the declarations stay in separate packages so a change to
	// one cannot silently change another.
	//
	// It is not this feature's code, and it is repeated rather than imported
	// because importing another feature's error codes would couple the two
	// packages in both directions.
	CodePasswordWorkUnavailable = "PASSWORD_WORK_UNAVAILABLE"
)
