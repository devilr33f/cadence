package providers

import (
	"errors"
	"regexp"
)

// detailRe is what a failure's detail may be: a token of codes and numbers
// (dial_dns, closed_1011, asr_error_3), never upstream text
var detailRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,63}$`)

// DetailError is a failure code with a codes-only detail of its cause, for
// spans and logs. Error and ErrorCode are the code alone, so a caller that
// compares the code sees what it always saw.
type DetailError struct {
	Code   string
	Detail string
}

func (e *DetailError) Error() string     { return e.Code }
func (e *DetailError) ErrorCode() string { return e.Code }

// Detail is err's detail when it carries a safe one, else "".
func Detail(err error) string {
	var d *DetailError
	if errors.As(err, &d) {
		return SafeDetail(d.Detail)
	}
	return ""
}

// SafeDetail is d when it is a token, else "".
func SafeDetail(d string) string {
	if detailRe.MatchString(d) {
		return d
	}
	return ""
}
