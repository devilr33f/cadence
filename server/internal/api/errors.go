package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
)

// ErrorBody is every admin error (M7a-D1): a snake_case code, when the error is
// about one field its JSON pointer, and optionally a detail token from a closed
// or validated set (ruling M7a-R1). It never carries a message, a cause or a
// value.
type ErrorBody struct {
	status int
	Code   string  `json:"error" doc:"A snake_case error code." example:"not_found"`
	Path   *string `json:"path,omitempty" doc:"The JSON pointer of the field the error is about." example:"/body/name"`
	Detail string  `json:"detail,omitempty" doc:"A snake_case token that refines the code, such as a device's own failure code." example:"no_session"`
}

// a detail is a token, never free text
var detailToken = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (e *ErrorBody) Error() string { return e.Code }

func (e *ErrorBody) GetStatus() int { return e.status }

// Error is a route's refusal: its status, its code and, for a field, the field's
// JSON pointer ("" is the whole document).
func Error(status int, code string, path ...string) huma.StatusError {
	e := &ErrorBody{status: status, Code: code}
	if len(path) > 0 {
		e.Path = &path[0]
	}
	return e
}

// ErrorWithDetail is a refusal refined by a detail token; a detail that is no
// token is left out.
func ErrorWithDetail(status int, code, detail string) huma.StatusError {
	e := &ErrorBody{status: status, Code: code}
	if detailToken.MatchString(detail) {
		e.Detail = detail
	}
	return e
}

// codes for the refusals huma makes itself; a handler's own pass through Error
var statusCodes = map[int]string{
	http.StatusBadRequest:            "invalid",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusForbidden:             "forbidden",
	http.StatusNotFound:              "not_found",
	http.StatusMethodNotAllowed:      "method_not_allowed",
	http.StatusNotAcceptable:         "not_acceptable",
	http.StatusRequestTimeout:        "timeout",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "too_large",
	http.StatusUnsupportedMediaType:  "unsupported_media_type",
	http.StatusUnprocessableEntity:   "invalid",
	http.StatusUpgradeRequired:       "upgrade_required",
	http.StatusTooManyRequests:       "rate_limited",
}

// statusCode is the model's code for a refusal of status: "internal" for a
// 5xx, "error" for a status no code names.
func statusCode(status int) string {
	if status >= http.StatusInternalServerError {
		return "internal"
	}
	if code, ok := statusCodes[status]; ok {
		return code
	}
	return "error"
}

// fiberErrors answers what reaches fiber's error handler under the admin
// prefix (fiber's own refusals: an unknown route or method, a body past the
// limit, a websocket route without an upgrade; and a plain route's error) in
// the model: a code alone, never fiber's plain text (a parse error's can quote
// the request) or a handler's cause. Elsewhere (the device api, the health
// check) fiber answers as ever.
func fiberErrors(c fiber.Ctx, err error) error {
	if !underAdmin(c.Path()) {
		return fiber.DefaultErrorHandler(c, err)
	}
	status := fiber.StatusInternalServerError
	var fe *fiber.Error
	if errors.As(err, &fe) {
		status = fe.Code
	}
	return refuse(c, status, statusCode(status))
}

// underAdmin holds for the admin prefix and what is below it, matched as
// fiber's router matches it (ascii case folded).
func underAdmin(path string) bool {
	n := len(adminPrefix)
	return len(path) >= n && asciiEqualFold(path[:n], adminPrefix) && (len(path) == n || path[n] == '/')
}

// huma's errors become the model: its messages and details can echo a value
// (a body, a parse error) and a handler's cause driver text, so only the
// status, a code and the first detail's location remain; a 5xx is "internal".
func init() {
	huma.NewError = func(status int, _ string, errs ...error) huma.StatusError {
		code := statusCode(status)
		if status >= http.StatusInternalServerError {
			return Error(status, code)
		}
		for _, err := range errs {
			// a resolver's own refusal is already the model
			var eb *ErrorBody
			if errors.As(err, &eb) {
				return eb
			}
			var d huma.ErrorDetailer
			if errors.As(err, &d) && d.ErrorDetail() != nil && d.ErrorDetail().Location != "" {
				return Error(status, code, pointer(d.ErrorDetail().Location))
			}
		}
		return Error(status, code)
	}
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return huma.NewError(status, msg, errs...)
	}
}

// pointer turns huma's location ("body.items[3].name") into a JSON pointer
// ("/body/items/3/name").
func pointer(location string) string {
	var b strings.Builder
	for _, part := range strings.Split(location, ".") {
		key, rest, _ := strings.Cut(part, "[")
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(key))
		for rest != "" {
			var index string
			index, rest, _ = strings.Cut(rest, "]")
			if _, err := strconv.Atoi(index); err == nil {
				b.WriteByte('/')
				b.WriteString(index)
			}
			rest = strings.TrimPrefix(rest, "[")
		}
	}
	return b.String()
}
