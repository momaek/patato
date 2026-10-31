// Package apierr 定义对外的错误格式：错误要让 Agent 能据此自行修正，而不只是给人看。
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func (e *Error) WithHint(format string, args ...any) *Error {
	c := *e
	c.Hint = fmt.Sprintf(format, args...)
	return &c
}

func New(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func BadRequest(code, format string, args ...any) *Error {
	return New(http.StatusBadRequest, code, format, args...)
}

func NotFound(code, format string, args ...any) *Error {
	return New(http.StatusNotFound, code, format, args...)
}

func Conflict(code, format string, args ...any) *Error {
	return New(http.StatusConflict, code, format, args...)
}

func Forbidden(code, format string, args ...any) *Error {
	return New(http.StatusForbidden, code, format, args...)
}

var (
	ErrUnauthorized = &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "需要登录或有效的 token"}
	ErrInternal     = &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "服务端错误", Retryable: true}
)

// From 把任意错误转成对外错误；未识别的错误一律视为内部错误。
func From(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal
}
