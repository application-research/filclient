package filclient

import "fmt"

type ErrorCode int

const (
	// Failed to connect to a miner.
	ErrMinerConnectionFailed ErrorCode = iota

	// There was an issue related to the Lotus API.
	ErrLotusError
)

type Error struct {
	Code  ErrorCode
	Inner error
}

func (code ErrorCode) String() string {
	var msg string

	switch code {
	case ErrMinerConnectionFailed:
		msg = "miner connection failed"
	case ErrLotusError:
		msg = "lotus error"
	default:
		msg = "(invalid error code)"
	}

	return msg
}

func (err *Error) Error() string {
	return fmt.Sprintf("%v: %v", err.Code.String(), err.Inner.Error())
}

func (err *Error) Unwrap() error {
	return err.Inner
}

func NewError(code ErrorCode, err error) *Error {
	return &Error{
		Code:  code,
		Inner: err,
	}
}

func NewErrMinerConnectionFailed(err error) error {
	return NewError(ErrMinerConnectionFailed, err)
}

func NewErrLotusError(err error) error {
	return NewError(ErrLotusError, err)
}
