package api

import "errors"

type ValidationErr struct {
	Err error
}

func (e ValidationErr) Error() string {
	return e.Err.Error()
}

func NewValidationErr(err string) error {
	return ValidationErr{Err: errors.New(err)}
}
