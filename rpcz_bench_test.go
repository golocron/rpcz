package rpcz_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/golocron/rpcz"
	"github.com/golocron/rpcz/internal/echo"
)

const (
	alpabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

	sz1k  = 1 << 10
	sz4k  = 4 << 10
	sz16k = 16 << 10
	sz32k = 32 << 10
	sz64k = 64 << 10
)

func randString(n int) string {
	result := make([]byte, n)
	abLen := int64(len(alpabet))

	for i := range result {
		if pos := rand.Int63() % abLen; pos < abLen {
			result[i] = alpabet[pos]
		}
	}

	return string(result)
}

func BenchmarkProtobuf_1K(b *testing.B) {
	benchmarkRPCZ(b, sz1k, "tcp", "127.0.0.1:11201", rpcz.Protobuf, sz1k)
}

func BenchmarkProtobuf_4K(b *testing.B) {
	benchmarkRPCZ(b, sz4k, "tcp", "127.0.0.1:11202", rpcz.Protobuf, sz4k)
}

func BenchmarkProtobuf_16K(b *testing.B) {
	benchmarkRPCZ(b, sz16k, "tcp", "127.0.0.1:11203", rpcz.Protobuf, sz16k+1)
}

func BenchmarkProtobuf_32K(b *testing.B) {
	benchmarkRPCZ(b, sz32k, "tcp", "127.0.0.1:11204", rpcz.Protobuf, sz32k+1)
}

func BenchmarkProtobuf_64K(b *testing.B) {
	benchmarkRPCZ(b, sz64k, "tcp", "127.0.0.1:11205", rpcz.Protobuf, sz64k+1)
}

func BenchmarkProtobufTLS_4K(b *testing.B) {
	benchmarkRPCZ(b, sz4k, "tls", "127.0.0.1:11202", rpcz.Protobuf, sz4k)
}

func BenchmarkProtobufNoBuf_4K(b *testing.B) {
	benchmarkRPCZ(b, sz4k, "tcp", "127.0.0.1:11202", rpcz.Protobuf, -1)
}

func BenchmarkJSON_1K(b *testing.B) {
	benchmarkRPCZ(b, sz1k, "tcp", "127.0.0.1:11301", rpcz.JSON, sz1k)
}

func BenchmarkJSON_4K(b *testing.B) {
	benchmarkRPCZ(b, sz4k, "tcp", "127.0.0.1:11302", rpcz.JSON, sz4k)
}

func BenchmarkJSON_16K(b *testing.B) {
	benchmarkRPCZ(b, sz16k, "tcp", "127.0.0.1:11303", rpcz.JSON, sz16k+1)
}

func BenchmarkJSONTLS_4K(b *testing.B) {
	benchmarkRPCZ(b, sz4k, "tls", "127.0.0.1:11302", rpcz.JSON, sz4k)
}

func benchmarkRPCZ(b *testing.B, tsize int, nw, addr string, kind rpcz.Encoding, bsize int) {
	const (
		svc = "Echo"
		mtd = "Echo"
	)

	srv, err := newServer(nw, addr, kind, bsize)
	if err != nil {
		b.Errorf("failed to init server: %v", err)
		b.FailNow()
	}

	esvc := echo.New()
	if err := srv.Register(esvc); err != nil {
		b.Errorf("failed to init server: %v", err)
		b.FailNow()
	}

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

	cl, err := newClient(nw, addr, kind, bsize)
	if err != nil {
		b.Errorf("failed to init client: %v", err)
		b.FailNow()
	}

	msg := randString(tsize)
	reqList := make(reqGen, 128)
	respList := make(respGen, 128)

	b.SetBytes(2 * int64(len(msg)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := reqList.obtainReq()
			req.Msg = msg

			resp := respList.obtainResp()

			if err := cl.SyncDo(ctx, svc, mtd, req, resp); err != nil {
				reqList.retainReq(req)
				respList.retainResp(resp)

				b.Errorf("failed to make req: %v", err)
				return
			}

			if m := resp.Msg; m != msg {
				reqList.retainReq(req)
				respList.retainResp(resp)

				b.Errorf("expected: %q\nactual: %q", msg, m)
				return
			}

			reqList.retainReq(req)
			respList.retainResp(resp)
		}
	})

	b.StopTimer()

	cl.Close()
	srv.Shutdown(ctx)
	wg.Wait()
}

func newServer(nw, addr string, kind rpcz.Encoding, bsize int) (*rpcz.Server, error) {
	cfg := &rpcz.ServerOptions{Encoding: kind, ReadBufSize: bsize, WriteBufSize: bsize}

	switch nw {
	case "tls":
		return newServerTLS(addr, cfg)
	default:
		return rpcz.NewServerWithOptions(addr, cfg)
	}
}

func newServerTLS(addr string, cfg *rpcz.ServerOptions) (*rpcz.Server, error) {
	cert, err := tls.LoadX509KeyPair("testdata/localhost.crt", "testdata/localhost.key")
	if err != nil {
		return nil, err
	}

	tcfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	return rpcz.NewServerTLSOptions(addr, tcfg, cfg)
}

func newClient(nw, addr string, kind rpcz.Encoding, bsize int) (*rpcz.Client, error) {
	cfg := &rpcz.ClientOptions{Encoding: kind, ReadBufSize: bsize, WriteBufSize: bsize}

	switch nw {
	case "tls":
		return newClientTLS(addr, cfg)
	default:
		return rpcz.NewClientWithOptions(addr, cfg)
	}
}

func newClientTLS(addr string, cfg *rpcz.ClientOptions) (*rpcz.Client, error) {
	cert, err := tls.LoadX509KeyPair("testdata/localhost.crt", "testdata/localhost.key")
	if err != nil {
		return nil, err
	}

	xcert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err

	}

	rcert := x509.NewCertPool()
	rcert.AddCert(xcert)

	tcfg := &tls.Config{RootCAs: rcert, ServerName: "localhost"}

	return rpcz.NewClientTLSOptions(addr, tcfg, cfg)
}

type reqGen chan *echo.EchoRequest

func (r reqGen) obtainReq() *echo.EchoRequest {
	select {
	case req := <-r:
		return req
	default:
		return &echo.EchoRequest{}
	}
}

func (r reqGen) retainReq(req *echo.EchoRequest) {
	req.Reset()

	select {
	case r <- req:
	default:
	}
}

type respGen chan *echo.EchoResponse

func (r respGen) obtainResp() *echo.EchoResponse {
	select {
	case req := <-r:
		return req
	default:
		return &echo.EchoResponse{}
	}
}

func (r respGen) retainResp(resp *echo.EchoResponse) {
	resp.Reset()

	select {
	case r <- resp:
	default:
	}
}
