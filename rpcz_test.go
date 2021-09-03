package rpcz

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	should "github.com/stretchr/testify/assert"
	must "github.com/stretchr/testify/require"

	"github.com/golocron/rpcz/internal/wire"
)

func TestServer_preStart(t *testing.T) {
	t.Run("invalid_options", func(t *testing.T) {
		srv := &Server{}
		err := srv.preStart()
		should.Equal(t, ErrInvalidServerOptions, err)
	})

	t.Run("invalid_addr", func(t *testing.T) {
		srv := &Server{cfg: &ServerOptions{}}
		err := srv.preStart()
		should.Equal(t, ErrInvalidAddr, err)
	})

	t.Run("invalid_addr", func(t *testing.T) {
		addr, err1 := net.ResolveTCPAddr("tcp4", "127.0.0.1:0")
		must.Equal(t, nil, err1)

		srv := &Server{cfg: &ServerOptions{}, addr: addr}
		err := srv.preStart()
		should.Equal(t, ErrInvalidListener, err)
	})

	t.Run("invalid_encoding", func(t *testing.T) {
		ln, err1 := net.Listen("tcp4", "127.0.0.1:0")
		must.Equal(t, nil, err1)
		defer ln.Close()

		srv := &Server{cfg: &ServerOptions{Encoding: Encoding(3)}, addr: ln.Addr(), ln: ln}
		err := srv.preStart()
		should.Equal(t, ErrInvalidEncoding, err)
	})

	t.Run("invalid_stopping", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		close(srv.stopping)

		err := srv.preStart()
		should.Equal(t, ErrSrvClosed, err)
	})

	t.Run("invalid_closed", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		close(srv.closed)

		err := srv.preStart()
		should.Equal(t, ErrSrvClosed, err)
	})

	t.Run("invalid_started", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		close(srv.started)

		err := srv.preStart()
		should.Equal(t, ErrSrvStarted, err)
	})

	t.Run("successful_call", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		err := srv.preStart()
		should.Equal(t, nil, err)
	})
}

func TestServer_Shutdown(t *testing.T) {
	t.Run("not_started", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		act := srv.Shutdown(context.TODO())
		should.Equal(t, nil, act)
	})

	t.Run("ln_nil", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)

		ln := srv.ln
		defer ln.Close()

		srv.ln = nil

		act := srv.Shutdown(context.TODO())
		should.Equal(t, nil, act)
	})

	t.Run("already_closed", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		ctx := context.TODO()
		wg := &sync.WaitGroup{}

		wg.Add(1)
		go func() {
			defer wg.Done()

			srv.Start(ctx)
		}()

		for !srv.Started() {
			time.Sleep(100 * time.Millisecond)
		}

		act1 := srv.Shutdown(ctx)
		should.Equal(t, nil, act1)

		act2 := srv.Shutdown(ctx)
		should.Equal(t, ErrSrvClosed, act2)

		wg.Wait()
	})

	t.Run("successful_call", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		ctx := context.TODO()
		wg := &sync.WaitGroup{}

		wg.Add(1)
		go func() {
			defer wg.Done()

			srv.Start(ctx)
		}()

		for !srv.Started() {
			time.Sleep(100 * time.Millisecond)
		}

		c01 := newConn("fake_01", &fakeRWC{})
		srv.addConn(c01)
		c01.toActive()

		c02 := newConn("fake_02", &fakeRWC{})
		srv.addConn(c02)
		c02.toActive()

		c03 := newConn("fake_03", &fakeRWC{})
		srv.addConn(c03)

		wg.Add(1)
		go func() {
			defer wg.Done()

			time.Sleep(20 * time.Millisecond)
			c01.toClosed()
			srv.delConn(c01)

			time.Sleep(20 * time.Millisecond)
			c02.toIdle()
		}()

		act := srv.Shutdown(ctx)
		should.Equal(t, nil, act)

		wg.Wait()
	})
}

func TestServer_closeIdle(t *testing.T) {
	t.Run("all_active", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		for _, name := range []string{"fake_01", "fake_02"} {
			c := newConn(name, &fakeRWC{})
			srv.addConn(c)
			c.toActive()
		}

		act := srv.closeIdle()
		should.Equal(t, false, act)
	})

	t.Run("successful_call", func(t *testing.T) {
		srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
		must.Equal(t, nil, err1)
		defer srv.ln.Close()

		c01 := newConn("fake_01", &fakeRWC{})
		srv.addConn(c01)
		c01.toActive()

		c02 := newConn("fake_02", &fakeRWC{})
		srv.addConn(c02)
		c02.toActive()

		c03 := newConn("fake_03", &fakeRWC{})
		srv.addConn(c03)

		srv.mu.Lock()
		should.Equal(t, len(srv.conns), 3)
		srv.mu.Unlock()

		act1 := srv.closeIdle()
		should.Equal(t, false, act1)

		srv.mu.Lock()
		should.Equal(t, len(srv.conns), 2)
		should.Equal(t, struct{}{}, srv.conns[c01])
		should.Equal(t, struct{}{}, srv.conns[c02])
		srv.mu.Unlock()

		c01.toIdle()
		act2 := srv.closeIdle()
		should.Equal(t, false, act2)

		srv.mu.Lock()
		should.Equal(t, len(srv.conns), 1)
		should.Equal(t, struct{}{}, srv.conns[c02])
		srv.mu.Unlock()

		c02.toIdle()
		act3 := srv.closeIdle()
		should.Equal(t, true, act3)

		srv.mu.Lock()
		should.Equal(t, len(srv.conns), 0)
		srv.mu.Unlock()
	})
}

func TestServer_shouldHangErr(t *testing.T) {
	type testCase struct {
		name  string
		given error
		exp   bool
	}

	tests := []*testCase{
		&testCase{name: "nil_false"},

		&testCase{name: "EOF_true", given: io.EOF, exp: true},

		&testCase{name: "UnexpectedEOF_true", given: io.ErrUnexpectedEOF, exp: true},

		&testCase{name: "CtxCanelled_true", given: context.Canceled, exp: true},

		&testCase{name: "CtxDeadlineExceeded_true", given: context.DeadlineExceeded, exp: true},

		&testCase{name: "any_false", given: errors.New("some error")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, err1 := NewServerWithOptions("127.0.0.1:0", &ServerOptions{})
			must.Equal(t, nil, err1)
			defer srv.ln.Close()

			act := srv.shouldHangErr(tc.given)
			should.Equal(t, tc.exp, act)
		})
	}
}

func TestNewService(t *testing.T) {
	t.Run("not_pointer", func(t *testing.T) {
		type tService MockService

		svc := tService{}

		act, err := newService(svc)
		should.Equal(t, errInvalidServiceType, err)
		should.Equal(t, (*service)(nil), act)
	})

	t.Run("unexported_type", func(t *testing.T) {
		type tService MockService

		svc := &tService{}

		act, err := newService(svc)
		should.Equal(t, errUnexportedService, err)
		should.Equal(t, (*service)(nil), act)
	})

	t.Run("no_methods", func(t *testing.T) {
		type Service MockService

		svc := &Service{}

		act, err := newService(svc)
		should.Equal(t, errInvalidServiceMethods, err)
		should.Equal(t, (*service)(nil), act)
	})

	t.Run("successful_call", func(t *testing.T) {
		svc := &MockService{}

		act, err := newService(svc)
		must.Equal(t, nil, err)

		should.Equal(t, "MockService", act.name)
		should.Equal(t, reflect.TypeOf(svc), act.recvt)
		should.Equal(t, reflect.ValueOf(svc), act.recvv)
		should.Equal(t, reflect.ValueOf(svc), act.recvv)

		mtd, ok := act.mset["UsableMethod"]
		must.Equal(t, true, ok)

		should.Equal(t, "UsableMethod", mtd.name)
		should.Equal(t, act.recvv, mtd.recvv)

		should.Equal(t, act.recvt.Method(8).Name, mtd.mtd.Name)
		should.Equal(t, act.recvt.Method(8).Type, mtd.mtd.Type)
		should.Equal(t, reflect.TypeOf(&UsableTypeArg{}), mtd.argt)
		should.Equal(t, reflect.TypeOf(&UsableTypeRepl{}), mtd.respt)
	})
}

func TestCreateMethods(t *testing.T) {
	t.Run("multiple_conditions", func(t *testing.T) {
		svc := &MockService{}

		recvt := reflect.TypeOf(svc)
		must.Equal(t, recvt.Kind(), reflect.Ptr)

		recvv := reflect.ValueOf(svc)

		mtds := createMethods(recvt, recvv)
		must.Equal(t, 1, len(mtds))

		mtd, ok := mtds["UsableMethod"]
		must.Equal(t, true, ok)

		should.Equal(t, "UsableMethod", mtd.name)
		should.Equal(t, recvv, mtd.recvv)
		should.Equal(t, recvt.Method(8).Name, mtd.mtd.Name)
		should.Equal(t, recvt.Method(8).Type, mtd.mtd.Type)
		should.Equal(t, reflect.TypeOf(&UsableTypeArg{}), mtd.argt)
		should.Equal(t, reflect.TypeOf(&UsableTypeRepl{}), mtd.respt)
	})
}

func TestBackoff(t *testing.T) {
	t.Run("successful_case", func(t *testing.T) {
		b := newDefaultBackoff()

		do := func() {
			for i := 0; i < 7; i++ {
				should.Equal(t, (10<<uint(i))*time.Millisecond, b.next())
			}

			for i := 0; i < 5; i++ {
				should.Equal(t, 1*time.Second, b.next())
			}
		}

		do()

		b.resetd()

		do()
	})

	t.Run("safe_nil", func(t *testing.T) {
		var b *backoff

		should.Equal(t, time.Duration(0), b.next())

		b.resetd()

		should.Equal(t, time.Duration(0), b.next())
	})
}

func TestCalcBufSize(t *testing.T) {
	type testCase struct {
		name  string
		given int
		exp   int
	}

	tests := []*testCase{
		&testCase{
			name:  "zero_8k",
			given: 0,
			exp:   8 << 10,
		},
		&testCase{
			name:  "ceil_4k_8k",
			given: 4 << 10,
			exp:   8 << 10,
		},
		&testCase{
			name:  "exact_8k",
			given: 8 << 10,
			exp:   8 << 10,
		},
		&testCase{
			name:  "ceil_8k_16k",
			given: (8 << 10) + 1,
			exp:   16 << 10,
		},
		&testCase{
			name:  "exact_16k",
			given: 16 << 10,
			exp:   16 << 10,
		},
		&testCase{
			name:  "ceil_to_32k",
			given: 24 << 10,
			exp:   32 << 10,
		},
		&testCase{
			name:  "ceil_to_128k",
			given: 100 << 10,
			exp:   128 << 10,
		},
		&testCase{
			name:  "ceil_to_256k",
			given: 129 << 10,
			exp:   256 << 10,
		},
		&testCase{
			name:  "ceil_to_512k",
			given: (256 << 10) + 1,
			exp:   512 << 10,
		},
		&testCase{
			name:  "floor_to_512k",
			given: (512 << 10) + 1,
			exp:   512 << 10,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			act := calcBufSize(tc.given)
			should.Equal(t, tc.exp, act)
		})
	}
}

func TestMarshalUnmarshal(t *testing.T) {
	t.Run("marshal_invalid_encoding", func(t *testing.T) {
		exp := &wire.Request{Id: 1, Service: "Valid"}
		_, err1 := marshal(Encoding(5), exp)
		should.Equal(t, errInvalidEncoding, err1)
	})

	t.Run("marshal_protobuf_nil", func(t *testing.T) {
		_, err1 := marshal(Protobuf, nil)
		should.Equal(t, errInvalidProtobufMsg, err1)
	})

	t.Run("marshal_protobuf_invalid", func(t *testing.T) {
		_, err1 := marshal(Protobuf, &crequestJSON{})
		should.Equal(t, errInvalidProtobufMsg, err1)
	})

	t.Run("protobuf_valid", func(t *testing.T) {
		exp := &wire.Request{Id: 1, Service: "Valid"}
		data, err1 := marshal(Protobuf, exp)
		should.Equal(t, nil, err1)

		t.Run("unmarshal_invalid_encoding", func(t *testing.T) {
			act := &wire.Request{}
			err2 := unmarshal(Encoding(5), data, act)
			should.Equal(t, errInvalidEncoding, err2)
		})

		t.Run("unmarshal_invalid", func(t *testing.T) {
			act := &crequestJSON{}
			err2 := unmarshal(Protobuf, data, act)
			should.Equal(t, errInvalidProtobufMsg, err2)
		})

		t.Run("unmarshal", func(t *testing.T) {
			act := &wire.Request{}
			err2 := unmarshal(Protobuf, data, act)
			must.Equal(t, nil, err2)
			should.Equal(t, exp.Id, act.Id)
			should.Equal(t, exp.Service, act.Service)
		})
	})

	t.Run("json_valid", func(t *testing.T) {
		exp := &wire.Request{Id: 1, Service: "Valid"}
		data, err1 := marshal(JSON, exp)
		should.Equal(t, nil, err1)

		t.Run("unmarshal", func(t *testing.T) {
			act := &wire.Request{}
			err2 := unmarshal(JSON, data, act)
			must.Equal(t, nil, err2)
			should.Equal(t, exp.Id, act.Id)
			should.Equal(t, exp.Service, act.Service)
		})
	})
}

// ---

type fakeRWC struct{}

func (*fakeRWC) Read(p []byte) (int, error) { return 0, nil }

func (*fakeRWC) Write(p []byte) (int, error) { return 0, nil }

func (*fakeRWC) Close() error { return nil }

type MockService struct{}

func (*MockService) unexported() {}

func (*MockService) CtxAndNotPtr(_ context.Context, arg2, arg3 string) int { return 0 }

func (*MockService) CtxUnsuablePtr(_ context.Context, arg2 *unsuableType, arg3 string) int {
	return 0
}

func (*MockService) CtxUsableArg2Arg3WrongReturn(_ context.Context, arg2 *UsableTypeArg, arg3 *UsableTypeRepl) int {
	return 0
}

func (*MockService) CtxUsableArg2NotPtr(_ context.Context, arg2 *UsableTypeArg, arg3 string) int {
	return 0
}

func (*MockService) CtxUsableArg2UnusablePtr(_ context.Context, arg2 *UsableTypeArg, arg3 *unsuableType) int {
	return 0
}

func (*MockService) FourArgsAndReturn(arg1, arg2, arg3 string) int { return 0 }

func (*MockService) FourArgsNoReturn(arg1, arg2, arg3 string) {}

func (*MockService) LessThan4Args(arg1, arg2 string) {}

func (*MockService) UsableMethod(ctx context.Context, arg2 *UsableTypeArg, arg3 *UsableTypeRepl) error {
	return nil
}

type unsuableType struct{}

type UsableTypeArg struct{}

type UsableTypeRepl struct{}
