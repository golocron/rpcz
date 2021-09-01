package rpcz

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"time"

	"github.com/golocron/rpcz/internal/wire"
)

// ServiceError represents an error returned by a remote method.
type ServiceError string

// Error returns the string representation of e.
func (e ServiceError) Error() string {
	return string(e)
}

// ClientOptions allows to setup Client.
//
// An empty value of ClientOptions uses safe defaults:
// - Protobuf encoding
// - 8192 bytes per connection for a read buffer
// - 8192 bytes per connection for a write buffer.
type ClientOptions struct {
	Encoding     Encoding
	ReadBufSize  int
	WriteBufSize int
}

// Copy returns a full copy of o.
func (o *ClientOptions) Copy() *ClientOptions {
	if o == nil {
		return &ClientOptions{}
	}

	result := &ClientOptions{
		Encoding:     o.Encoding,
		ReadBufSize:  o.ReadBufSize,
		WriteBufSize: o.WriteBufSize,
	}

	return result
}

func (o *ClientOptions) check() error {
	if o == nil {
		return ErrInvalidClientOptions
	}

	if !checkEncoding(o.Encoding) {
		return ErrInvalidEncoding
	}

	o.adjReadBufSize()
	o.adjWriteBufSize()

	return nil
}

func (o *ClientOptions) adjReadBufSize() {
	o.ReadBufSize = calcBufSize(o.ReadBufSize)
}

func (o *ClientOptions) adjWriteBufSize() {
	o.WriteBufSize = calcBufSize(o.WriteBufSize)
}

// Client makes requests to remote methods on services registered on a remote server.
type Client struct {
	cfg *ClientOptions
	c   *conn
	enc clientEncoder

	onceStop  *sync.Once
	onceClose *sync.Once
	onceDone  *sync.Once
	stopping  chan struct{}
	closed    chan struct{}
	recvDone  chan struct{}

	reqList requestRetainer

	mu      *sync.Mutex
	id      uint64
	results map[uint64]*Result
}

// NewClient returns a client connected to saddr with Protobuf encoding and default options.
func NewClient(saddr string) (*Client, error) {
	return NewClientWithOptions(saddr, &ClientOptions{})
}

// NewClientWithOptions returns a client connected to saddr and set up with opts.
func NewClientWithOptions(saddr string, opts *ClientOptions) (*Client, error) {
	nc, err := net.Dial("tcp", saddr)
	if err != nil {
		return nil, err
	}

	return NewClientWithConn(opts, nc)
}

// NewClientTLS returns a client connected to saddr in TLS mode.
func NewClientTLS(saddr string, cfg *tls.Config) (*Client, error) {
	return NewClientTLSOptions(saddr, cfg, &ClientOptions{})
}

// NewClientTLSOptions creates a client connected to saddr over TLS with opts.
func NewClientTLSOptions(saddr string, cfg *tls.Config, opts *ClientOptions) (*Client, error) {
	nc, err := tls.Dial("tcp", saddr, cfg)
	if err != nil {
		return nil, err
	}

	return NewClientWithConn(opts, nc)
}

// NewClientWithConn creates a client set up with opts connected to nc.
func NewClientWithConn(opts *ClientOptions, nc net.Conn) (*Client, error) {
	cfg := opts.Copy()
	if err := cfg.check(); err != nil {
		return nil, err
	}

	c := newConn(nc.RemoteAddr().String(), nc)
	result := &Client{
		cfg: cfg,
		c:   c,
		enc: newClientEncoder(cfg.Encoding, newBufReader(c.rwc, cfg.ReadBufSize), newBufWriter(c.rwc, cfg.WriteBufSize)),

		onceStop:  &sync.Once{},
		onceClose: &sync.Once{},
		onceDone:  &sync.Once{},
		stopping:  make(chan struct{}),
		closed:    make(chan struct{}),
		recvDone:  make(chan struct{}),

		reqList: newRequestRetainer(retainRequestNum),

		mu:      &sync.Mutex{},
		results: make(map[uint64]*Result),
	}

	go result.recv(context.Background())

	return result, nil
}

// Do asynchronously calls mtd on svc with given args and resp.
func (c *Client) Do(ctx context.Context, svc, mtd string, args, resp interface{}) *Result {
	result := newResult(svc, mtd, args, resp)

	c.send(ctx, result)

	return result
}

// SyncDo calls Do and awaits for the response.
func (c *Client) SyncDo(ctx context.Context, svc, mtd string, args, resp interface{}) error {
	result := c.Do(ctx, svc, mtd, args, resp)

	return result.Err()
}

// Peer returns the address of the server c is connected to.
func (c *Client) Peer() string {
	if c.c == nil {
		return ""
	}

	return c.c.raddr
}

// Close closes the connection and shuts down the client.
//
// Any unfinished requests are aborted.
func (c *Client) Close() error {
	var err error

	c.closeRecv()

	c.onceClose.Do(func() {
		closeOrSkip(c.closed)
		err = c.c.close()
	})

	for !c.isRecvDone() {
		time.Sleep(closeLoopDelay)
	}

	return err
}

func (c *Client) send(ctx context.Context, fut *Result) {
	if c.isStopping() {
		fut.done(ErrShutdown)
		return
	}

	if c.isClosed() {
		fut.done(ErrClosed)
		return
	}

	c.mu.Lock()
	id := c.id
	c.id++
	c.results[id] = fut
	c.mu.Unlock()

	req := c.reqList.obtainReq()
	req.Kind = int32(c.cfg.Encoding)
	req.Id = id
	req.Service = fut.svc
	req.Method = fut.mtd

	if err := c.enc.writeRequest(ctx, req, fut.args); err != nil {
		c.mu.Lock()
		fut = c.results[id]
		delete(c.results, id)
		c.mu.Unlock()

		if fut != nil {
			fut.done(err)
		}
	}

	c.reqList.retainReq(req)
}

func (c *Client) recv(ctx context.Context) {
	var (
		err  error
		rerr error
		resp = &wire.Response{}
	)

	for {
		select {
		case <-c.recvDone:
			return
		case <-c.stopping:
			c.closeRecv()
			return
		case <-c.closed:
			c.closeRecv()
			return
		case <-ctx.Done():
			c.closeRecv()
			c.stop(ctx.Err())

			return
		default:
			*resp = wire.Response{}
			if err = c.enc.readResponse(ctx, resp); err != nil {
				if c.isClosed() && c.isRecvDone() {
					c.stop(ErrClosed)

					return
				}

				c.closeRecv()
				c.stop(err)

				return
			}

			c.mu.Lock()
			fut := c.results[resp.Id]
			delete(c.results, resp.Id)
			c.mu.Unlock()

			switch {
			case fut == nil:
				// No op.
			case resp.Error != "":
				rerr = ServiceError(resp.Error)
				fut.done(rerr)
			default:
				if err = unmarshal(Encoding(resp.Kind), resp.Data, fut.resp); err != nil {
					fut.done(err)

					continue
				}

				fut.done(nil)
			}
		}
	}
}

func (c *Client) stop(err error) {
	c.onceStop.Do(func() { closeOrSkip(c.stopping) })

	c.mu.Lock()
	for k, fut := range c.results {
		delete(c.results, k)
		fut.done(err)
	}
	c.mu.Unlock()
}

func (c *Client) closeRecv() {
	c.onceDone.Do(func() { closeOrSkip(c.recvDone) })
}

func (c *Client) isStopping() bool {
	select {
	case <-c.stopping:
		return true
	default:
		return false
	}
}

func (c *Client) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *Client) isRecvDone() bool {
	select {
	case <-c.recvDone:
		return true
	default:
		return false
	}
}

// Result represents a result of a request.
type Result struct {
	svc  string
	mtd  string
	args interface{}
	resp interface{}
	err  chan error
}

func newResult(svc, mtd string, args, resp interface{}) *Result {
	result := &Result{
		svc:  svc,
		mtd:  mtd,
		args: args,
		resp: resp,
		err:  make(chan error, 1),
	}

	return result
}

// Err waits for the request to finish and returns nil on success, and an error otherwise.
func (r *Result) Err() error {
	return <-r.err
}

// ErrChan returns a channel that indicates completion of the request by sending a nil or non-nil error.
func (r *Result) ErrChan() <-chan error {
	return r.err
}

func (r *Result) done(err error) {
	r.err <- err
	close(r.err)
}
