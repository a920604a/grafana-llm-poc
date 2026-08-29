package llm

import "errors"

var (
	ErrUnavailable      = errors.New("llm unavailable")
	ErrUnauthorized     = errors.New("llm unauthorized")
	ErrRateLimited      = errors.New("llm rate limited")
	ErrProtocol         = errors.New("llm protocol error")
	ErrResponseTooLarge = errors.New("llm response too large")
)
