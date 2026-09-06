package vconn

import "errors"

func errorsAs(err error, target any) bool { return errors.As(err, target) }
