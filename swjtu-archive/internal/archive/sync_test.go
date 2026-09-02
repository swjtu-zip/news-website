package archive

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

type timeoutResourceError struct{}

func (timeoutResourceError) Error() string   { return "resource timeout" }
func (timeoutResourceError) Timeout() bool   { return true }
func (timeoutResourceError) Temporary() bool { return true }

var _ net.Error = timeoutResourceError{}

func TestResourceRetryable(t *testing.T) {
	if resourceRetryable(errors.New("connection reset")) != true {
		t.Fatal("connection reset should be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", timeoutResourceError{})) {
		t.Fatal("timeout should not be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatal("deadline should not be retried")
	}
	if resourceRetryable(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Fatal("cancellation should not be retried")
	}
}
