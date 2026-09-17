package session

import "time"

const (
	SessionAbsoluteLifetime = 90 * 24 * time.Hour
	SessionIdleLifetime     = 30 * 24 * time.Hour
	AccessTokenLifetime     = 15 * time.Minute
	SessionListDefaultLimit = 20
	SessionListMaxLimit     = 50
)