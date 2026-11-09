package watchservice

import "errors"

type startFailure struct {
	reason string
	err    error
}

func (e *startFailure) Error() string { return e.err.Error() }
func (e *startFailure) Unwrap() error { return e.err }
func StartFailureReason(err error) string {
	var e *startFailure
	if errors.As(err, &e) {
		return e.reason
	}
	return ""
}
func windowsStartFailure(reason string, err error) error {
	return &startFailure{reason: reason, err: err}
}
