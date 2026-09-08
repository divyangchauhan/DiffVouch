package dv

import "errors"

const (
	ExitGate        = 1
	ExitArguments   = 2
	ExitGit         = 3
	ExitProvider    = 4
	ExitGitHub      = 5
	ExitCoverage    = 6
	ExitInterrupted = 130
)

type Error struct {
	Code int
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	if e.Msg == "" {
		return e.Err.Error()
	}
	return e.Msg + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func New(code int, msg string) error { return &Error{Code: code, Msg: msg} }

func Wrap(code int, msg string, err error) error {
	return &Error{Code: code, Msg: msg, Err: err}
}

func ExitCode(err error) int {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ExitArguments
}
