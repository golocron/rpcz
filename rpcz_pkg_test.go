package rpcz_test

import (
	"context"
	"sync"
	"testing"
	"time"

	should "github.com/stretchr/testify/assert"
	must "github.com/stretchr/testify/require"

	"github.com/golocron/rpcz"
	"github.com/golocron/rpcz/internal/echo"
)

func TestServer_Full_Protobuf_0K(t *testing.T) {
	testServer_full(t, "tcp", "127.0.0.1:10001", rpcz.Protobuf, -1)
}

func TestServer_Full_Protobuf_4K(t *testing.T) {
	testServer_full(t, "tcp", "127.0.0.1:10001", rpcz.Protobuf, 4<<10)
}

func TestServer_Full_ProtobufTLS_4K(t *testing.T) {
	testServer_full(t, "tls", "127.0.0.1:10001", rpcz.Protobuf, 4<<10)
}

func TestServer_Full_JSON_0K(t *testing.T) {
	testServer_full(t, "tcp", "127.0.0.1:10001", rpcz.JSON, -1)
}

func TestServer_Full_JSON_4K(t *testing.T) {
	testServer_full(t, "tcp", "127.0.0.1:10001", rpcz.JSON, 4<<10)
}

func TestServer_Full_JSONTLS_4K(t *testing.T) {
	testServer_full(t, "tls", "127.0.0.1:10001", rpcz.JSON, 4<<10)
}

func TestServerClient_Errors(t *testing.T) {
	const (
		nw    = "tcp"
		addr  = "127.0.0.1:10003"
		kind  = rpcz.Protobuf
		bsize = 4 << 10
	)

	t.Run("server_stops_early", func(t *testing.T) {
		srv, err1 := newServer(nw, addr, kind, bsize)
		must.Equal(t, nil, err1)

		svc := echo.New()
		err2 := srv.Register(svc)
		must.Equal(t, nil, err2)

		ctx := context.TODO()
		wg := &sync.WaitGroup{}

		wg.Add(1)
		go func() {
			defer wg.Done()

			if err3 := srv.Start(ctx); err3 != nil {
				t.Logf("srv.Start returned unexpected error: %s", err3)
			}
		}()

		for !srv.Started() {
			time.Sleep(100 * time.Millisecond)
		}

		cl, err4 := newClient(nw, addr, kind, bsize)
		must.Equal(t, nil, err4)

		err5 := srv.Shutdown(ctx)
		should.Equal(t, nil, err5)

		req := &echo.EchoRequest{Msg: "This is the wrong Way"}
		resp := &echo.EchoResponse{}

		err6 := cl.SyncDo(ctx, "Echo", "Echo", req, resp)
		should.Error(t, err6)

		wg.Wait()

		err7 := cl.Close()
		should.Equal(t, nil, err7)
	})
}

func testServer_full(t *testing.T, nw, addr string, kind rpcz.Encoding, bsize int) {
	srv, err1 := newServer(nw, addr, kind, bsize)
	must.Equal(t, nil, err1)

	svc := echo.New()
	err2 := srv.Register(svc)
	must.Equal(t, nil, err2)

	ctx := context.TODO()
	wg := &sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()

		if err3 := srv.Start(ctx); err3 != nil {
			t.Logf("srv.Start returned unexpected error: %s", err3)
		}
	}()

	for !srv.Started() {
		time.Sleep(100 * time.Millisecond)
	}

	cl, err4 := newClient(nw, addr, kind, bsize)
	must.Equal(t, nil, err4)

	peer := cl.Peer()
	should.Equal(t, addr, peer)

	type tcGiven struct {
		svc string
		mtd string
		msg string
	}

	type tcExpected struct {
		msg string
		err error
	}

	type testCase struct {
		name  string
		given *tcGiven
		exp   *tcExpected
	}

	tests := []*testCase{
		&testCase{
			name:  "invalid_service_requested",
			given: &tcGiven{mtd: "Echo"},

			exp: &tcExpected{err: rpcz.ServiceError("rpcz: invalid service")},
		},

		&testCase{
			name:  "unknown_service_requested",
			given: &tcGiven{svc: "sdsds", mtd: "Echo"},

			exp: &tcExpected{err: rpcz.ServiceError("rpcz: unknown service")},
		},

		&testCase{
			name:  "invalid_method_requested",
			given: &tcGiven{svc: "Echo"},

			exp: &tcExpected{err: rpcz.ServiceError("rpcz: invalid service method")},
		},

		&testCase{
			name:  "unknown_method_requested",
			given: &tcGiven{svc: "Echo", mtd: "sdsds"},

			exp: &tcExpected{err: rpcz.ServiceError("rpcz: unknown method")},
		},

		&testCase{
			name:  "method_returns_error",
			given: &tcGiven{svc: "Echo", mtd: "Echo"},

			exp: &tcExpected{err: rpcz.ServiceError("echo: invalid message")},
		},

		&testCase{
			name:  "successful_call",
			given: &tcGiven{svc: "Echo", mtd: "Echo", msg: "This is the Way"},

			exp: &tcExpected{msg: "This is the Way"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &echo.EchoRequest{Msg: tc.given.msg}
			resp := &echo.EchoResponse{}

			err := cl.SyncDo(ctx, tc.given.svc, tc.given.mtd, req, resp)
			should.Equal(t, tc.exp.err, err)

			if tc.exp.err != nil {
				return
			}

			should.Equal(t, tc.exp.msg, resp.Msg)
		})
	}

	err5 := cl.Close()
	should.Equal(t, nil, err5)

	err6 := srv.Shutdown(ctx)
	should.Equal(t, nil, err6)

	wg.Wait()
}
