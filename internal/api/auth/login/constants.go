package login

import "time"

const (
	sessionAbsoluteLifetime = 90 * 24 * time.Hour
	accessTokenLifetime     = 15 * time.Minute
)