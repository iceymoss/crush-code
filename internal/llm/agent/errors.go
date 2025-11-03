package agent

import (
	"context"
	"errors"
)

var (
	// ErrRequestCancelled 请求被用户取消
	ErrRequestCancelled = errors.New("request canceled by user")

	// ErrSessionBusy 会话当前正在处理另一个请求
	ErrSessionBusy = errors.New("session is currently processing another request")
)

func isCancelledErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, ErrRequestCancelled)
}
