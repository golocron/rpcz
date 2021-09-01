// package echo shows a simple RPC service that can be served with rpcz.
package echo

import (
	"context"
	"errors"
	"time"
)

const (
	defaultTimeout = 60 * time.Second
)

var (
	errInvalidMsg = errors.New("echo: invalid message")
)

// Echo service replies back with the message it receives.
type Echo struct{}

// New returns an instance of Echo.
func New() *Echo { return &Echo{} }

// Echo handles req and fills in resp.
func (s *Echo) Echo(ctx context.Context, req *EchoRequest, resp *EchoResponse) error {
	if req.GetMsg() == "" {
		return errInvalidMsg
	}

	resp.Msg = req.Msg

	return nil
}

// ExtendedEcho service replies back with the message it receives.
//
// It shows an example of service-side and caller-defined timeouts.
type ExtendedEcho struct {
	echo    *Echo
	timeout time.Duration
}

// NewExtendedEcho returns a new ExtendedEcho with the specified timeout.
func NewExtendedEcho(timeout time.Duration) *ExtendedEcho {
	result := &ExtendedEcho{timeout: timeout}
	if result.timeout == 0 {
		result.timeout = defaultTimeout
	}

	return result
}

// Echo handles req and fills in resp.
//
// The service may wait for req.Delay, if specified, but no longer than s.timeout.
func (s *ExtendedEcho) Echo(ctx context.Context, req *EchoRequest, resp *EchoResponse) error {
	return s.handle(ctx, req, resp)
}

func (s *ExtendedEcho) handle(ctx context.Context, req *EchoRequest, resp *EchoResponse) error {
	lctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	out := s.do(ctx, req, resp)

	select {
	case <-lctx.Done():
		return lctx.Err()
	case err := <-out:
		return err
	}
}

func (s *ExtendedEcho) do(ctx context.Context, req *EchoRequest, resp *EchoResponse) chan error {
	out := make(chan error, 1)

	go doEcho(ctx, out, s.echo, req, resp)

	return out
}

func doEcho(ctx context.Context, dst chan<- error, s *Echo, req *EchoRequest, resp *EchoResponse) {
	defer close(dst)

	if req.Delay <= 0 {
		dst <- s.Echo(ctx, req, resp)
		return
	}

	timer := time.NewTimer(time.Duration(req.Delay))
	defer func() { _ = timer.Stop() }()

	select {
	case <-ctx.Done():
		dst <- ctx.Err()
		return
	case <-timer.C:
	}

	dst <- s.Echo(ctx, req, resp)
}
