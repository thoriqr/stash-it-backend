package testutil

import "net"

// splitHostPort separates a host:port endpoint.
//
// It is a named wrapper rather than a direct net.SplitHostPort call only so the
// failure message can name what was being parsed; the Redis container reports its
// endpoint this way and a malformed one otherwise surfaces as a connection error
// that reads like Redis being unavailable.
func splitHostPort(endpoint string) (string, string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", "", err
	}

	return host, port, nil
}
