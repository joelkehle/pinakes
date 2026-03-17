package bus

import (
	"fmt"
	"time"
)

const (
	CodeValidation   = "validation"
	CodeUnauthorized = "unauthorized"
	CodeNotFound     = "not_found"
	CodeRejected     = "rejected"
	CodeRateLimited  = "rate_limited"
	CodeUnavailable  = "unavailable"
	CodeTimeout      = "timeout"
	CodeInternal     = "internal"
)

type Error struct {
	Code       string
	Message    string
	Transient  bool
	RetryAfter int
	Status     int
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func statusForCode(code string) int {
	switch code {
	case CodeValidation:
		return 400
	case CodeUnauthorized:
		return 401
	case CodeNotFound:
		return 404
	case CodeRejected:
		return 409
	case CodeRateLimited:
		return 429
	case CodeTimeout:
		return 408
	case CodeUnavailable:
		return 503
	default:
		return 500
	}
}

func newError(code, message string, transient bool, retryAfter time.Duration) *Error {
	retryAfterSec := 0
	if retryAfter > 0 {
		retryAfterSec = int(retryAfter.Seconds())
		if retryAfterSec <= 0 {
			retryAfterSec = 1
		}
	}
	return &Error{
		Code:       code,
		Message:    message,
		Transient:  transient,
		RetryAfter: retryAfterSec,
		Status:     statusForCode(code),
	}
}

func NewValidationJSONError(err error) error {
	return newError(CodeValidation, "invalid json: "+err.Error(), false, 0)
}

func NewInternalError(message string) error {
	return newError(CodeInternal, message, true, 0)
}
