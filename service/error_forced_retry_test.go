package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
)

// These signatures are a NetMind-local patch that must survive every upstream
// rebase. Upstream has no equivalent, and a silently dropped signature turns a
// recoverable upstream stale-state error into a hard client-facing failure, so
// pin the behaviour with a test rather than relying on the port being noticed.
func TestIsUpstreamForcedRetryError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"nested affinity disabled", "channel selected by channel affinity has been disabled", true},
		{"nested channel disabled 403", "This channel has been disabled", true},
		{"bedrock invalid beta flag", "ValidationException: invalid beta flag: foo", true},
		{"anthropic beta header rejected", "Unexpected value(s) `x` for the `anthropic-beta` header", true},
		{"bedrock operation not allowed", "Operation not allowed", true},
		{"substring match inside larger body", `{"error":{"message":"upstream said invalid beta flag here"}}`, true},
		{"unrelated error", "context deadline exceeded", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := types.NewError(errors.New(tc.msg), types.ErrorCodeBadResponse)
			if got := IsUpstreamForcedRetryError(err); got != tc.want {
				t.Fatalf("IsUpstreamForcedRetryError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

func TestIsUpstreamForcedRetryErrorNil(t *testing.T) {
	if IsUpstreamForcedRetryError(nil) {
		t.Fatal("nil error must not force a retry")
	}
}
