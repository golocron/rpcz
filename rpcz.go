/*
	Package rpcz allows exposing and accessing methods of a service over network.

	This package supports communication over TCP, Unix sockets, and with TLS on top of it.
	The default and recommended encoding is Protobuf, with JSON available as an alternative.
	No user-defined encodings (aka codecs) are supported, nor it is planned.
	It is as an alternative to rpc from the standard library and gRPC.

	The server allows to register and expose one or more services. A service must be a pointer to a value of an exported type.
	Each method meeting the following criteria will be registered and can be called remotely:

		- A method must be exported.
		- A method has three arguments.
		- The first argument is context.Context.
		- The second and third arguments are both pointers and exported.
		- A method has only one return parameter of the type error.

	The following line illustrates a valid method's signature:

		func (s *Service) SomeMethod(ctx context.Context, req *SomeMethodRequest, resp *SomeMethodResponse) error

	The request and response types must be marshallable to Protobuf, i.e. implement the proto.Message interface.

	The first argument of a service's method is passed in the server's context. This context is cancelled when
	the server is requested to shutdown. The server is not concerned with specifying timeouts for service's methods.
	It's the service's responsibility to implement timeouts should they be needed. The primary use of the parent context
	is to be notified when shutdown has been requested to gracefully finish any ongoing operations.

	At the moment, the context does not include any data, but it might be exteneded at a later point with useful information
	such as a request trace identificator. Reminder: contextes MUST NOT be used for dependency injection.

	The second argument represents a request to the method of a service. The third argument is passed in a pointer to a value
	to which the method writes the response.

	If a method returns an error, it's sent over the wire as a string, and when the client returns it as ServiceError.
*/
package rpcz

import (
	"context"
	"crypto/tls"
	"errors"
	"go/token"
	"io"
	"net"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golocron/rpcz/internal/wire"
)

const (
	// Supported encodings.
	Unknown Encoding = -1 + iota
	Protobuf
	JSON
)

const (
	bufSize8k   = 8 << 10
	bufSize256k = 256 << 10
	bufSize512k = 512 << 10
	defBufSize  = bufSize8k
	maxBufSize  = bufSize512k

	retainRequestNum  = 256
	retainResponseNum = 256

	shutdownLoopDelay = 10 * time.Millisecond
	closeLoopDelay    = 5 * time.Millisecond

	msgInvalidReturnNum  = "rpcz: method returned invalid number of return values"
	msgInvalidReturnType = "rpcz: method returned invalid type"
	msgUnexpectedError   = "rpcz: unexpected error"
)

var (
	ErrInvalidEncoding      = errors.New("rpcz: invalid encoding")
	ErrInvalidServerOptions = errors.New("rpcz: invalid server options: must not be nil")

	ErrInvalidAddr     = errors.New("rpcz: invalid server address")
	ErrInvalidListener = errors.New("rpcz: invalid server listener")
	ErrSrvStarted      = errors.New("rpcz: server already started")
	ErrSrvClosed       = errors.New("rpcz: server already shutdown")

	ErrInvalidClientOptions = errors.New("rpcz: invalid client options: must not be nil")
	ErrShutdown             = errors.New("rpcz: client is shutdown")
	ErrClosed               = errors.New("rpcz: client is closed")

	errInvalidEncoding    = errors.New("rpcz: invalid encoding")
	errInvalidProtobufMsg = errors.New("rpcz: invalid protobuf message")

	errInvalidServiceName    = errors.New("rpcz: invalid service name")
	errUnexportedService     = errors.New("rpcz: service is not exported")
	errInvalidServiceType    = errors.New("rpcz: service is not a pointer")
	errInvalidServiceMethods = errors.New("rpcz: service has no valid methods")

	errInvalidReqServiceName   = errors.New("rpcz: invalid service")
	errInvalidReqServiceMethod = errors.New("rpcz: invalid service method")
	errUnknownReqService       = errors.New("rpcz: unknown service")
	errUnknownReqServiceMethod = errors.New("rpcz: unknown method")
)

// Encoding specifies which encoding is supported by a concrete server, client, and transport.
type Encoding int8

// ServerOptions allows to setup Server.
//
// An empty value of ServerOptions uses safe defaults:
// - Protobuf encoding
// - 8192 bytes per connection for a read buffer
// - 8192 bytes per connection for a write buffer.
type ServerOptions struct {
	Encoding     Encoding
	ReadBufSize  int
	WriteBufSize int
}

// Copy returns a full copy of o.
func (o *ServerOptions) Copy() *ServerOptions {
	if o == nil {
		return &ServerOptions{}
	}

	result := &ServerOptions{
		Encoding:     o.Encoding,
		ReadBufSize:  o.ReadBufSize,
		WriteBufSize: o.WriteBufSize,
	}

	return result
}

func (o *ServerOptions) check() error {
	if o == nil {
		return ErrInvalidServerOptions
	}

	if !checkEncoding(o.Encoding) {
		return ErrInvalidEncoding
	}

	o.adjReadBufSize()
	o.adjWriteBufSize()

	return nil
}

func (o *ServerOptions) adjReadBufSize() {
	o.ReadBufSize = calcBufSize(o.ReadBufSize)
}

func (o *ServerOptions) adjWriteBufSize() {
	o.WriteBufSize = calcBufSize(o.WriteBufSize)
}

// Server handles requests from clients by calling methods on registered services.
type Server struct {
	cfg  *ServerOptions
	addr net.Addr
	ln   net.Listener

	onceStart *sync.Once
	onceStop  *sync.Once
	onceClose *sync.Once
	started   chan struct{}
	stopping  chan struct{}
	closed    chan struct{}

	rbufPool *sync.Pool
	wbufPool *sync.Pool

	reqList  requestRetainer
	respList responseRetainer

	mu    *sync.Mutex
	conns map[*conn]struct{}

	smu  *sync.Mutex
	sset map[string]*service

	wg *sync.WaitGroup
}

// NewServer returns a server listening on laddr with Protobuf encoding and default options.
func NewServer(laddr string) (*Server, error) {
	return NewServerWithOptions(laddr, &ServerOptions{})
}

// NewServerWithOptions returns a server listening on laddr and set up with opts.
func NewServerWithOptions(laddr string, opts *ServerOptions) (*Server, error) {
	ln, err := net.Listen("tcp", laddr)
	if err != nil {
		return nil, err
	}

	return NewServerWithListener(opts, ln)
}

// NewServerTLS returns a server listening on laddr in TLS mode.
func NewServerTLS(laddr string, cfg *tls.Config) (*Server, error) {
	return NewServerTLSOptions(laddr, cfg, &ServerOptions{})
}

// NewServerTLSOptions creates a server listening on laddr in TLS mode with opts.
func NewServerTLSOptions(laddr string, cfg *tls.Config, opts *ServerOptions) (*Server, error) {
	ln, err := tls.Listen("tcp", laddr, cfg)
	if err != nil {
		return nil, err
	}

	return NewServerWithListener(opts, ln)
}

// NewServerWithListener returns a server set up with opts listening on ln.
func NewServerWithListener(opts *ServerOptions, ln net.Listener) (*Server, error) {
	cfg := opts.Copy()
	if err := cfg.check(); err != nil {
		return nil, err
	}

	result := newServer(cfg, ln)

	return result, nil
}

func newServer(cfg *ServerOptions, ln net.Listener) *Server {
	result := &Server{
		cfg:  cfg,
		addr: ln.Addr(),
		ln:   ln,

		onceStart: &sync.Once{},
		onceStop:  &sync.Once{},
		onceClose: &sync.Once{},
		started:   make(chan struct{}),
		stopping:  make(chan struct{}),
		closed:    make(chan struct{}),

		mu:    &sync.Mutex{},
		conns: make(map[*conn]struct{}),

		smu:  &sync.Mutex{},
		sset: make(map[string]*service),

		wg:       &sync.WaitGroup{},
		rbufPool: &sync.Pool{New: func() interface{} { return newBufReader(nil, cfg.ReadBufSize) }},
		wbufPool: &sync.Pool{New: func() interface{} { return newBufWriter(nil, cfg.WriteBufSize) }},

		reqList:  newRequestRetainer(retainRequestNum),
		respList: newResponseRetainer(retainResponseNum),
	}

	return result
}

// Register registers the given svc.
//
// The svc is registered if it's exported and at least one method satisfies the following conditions:
//	- a method is exported
//	- a method has three arguments
//	- the first argument is context.Context
//	- the second and third arguments are both pointers to exported values that implement proto.Message
//	- a method has only one return parameter of the type error.
// New services can be added while the server is runnning.
func (s *Server) Register(svc interface{}) error {
	rsvc, err := newService(svc)
	if err != nil {
		return err
	}

	s.smu.Lock()
	s.sset[rsvc.name] = rsvc
	s.smu.Unlock()

	return nil
}

// Start starts accepting connections.
//
// After a successful call to Start, subsequent calls to return ErrServerStarted.
func (s *Server) Start(ctx context.Context) error {
	if err := s.preStart(); err != nil {
		return err
	}

	s.onceStart.Do(func() { closeOrSkip(s.started) })

	boff := newDefaultBackoff()

	for {
		select {
		case <-s.stopping:
			return nil
		case <-s.closed:
			return nil
		case <-ctx.Done():
			return ctx.Err()

		default:
			nc, err := s.ln.Accept()
			if err != nil {
				nerr, ok := err.(net.Error)
				if !ok || !nerr.Temporary() {
					if s.isStopping() || s.isClosed() {
						return nil
					}

					return err
				}

				select {
				case <-s.stopping:
					return ErrSrvClosed
				case <-s.closed:
					return ErrSrvClosed
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(boff.next()):
				}

				continue
			}

			boff.resetd()

			c := newConn(nc.RemoteAddr().String(), nc)
			s.addConn(c)

			enc := newServerEncoder(s.cfg.Encoding, s.obtainBufReader(c.rwc), s.obtainBufWriter(c.rwc))

			s.wg.Add(1)
			go s.handle(ctx, c, enc)
		}
	}

	s.wg.Wait()

	return nil
}

// Run is an alias for Start.
func (s *Server) Run(ctx context.Context) error {
	return s.Start(ctx)
}

// Shutdown gracefully shuts down the server. A successful call returns no error.
//
// It iteratively tries to close new and idle connections until no such left,
// unless/until interrupted by cancellation of ctx.
// Subsequent calls return ErrSrvClosed.
func (s *Server) Shutdown(ctx context.Context) error {
	if !s.isStarted() || s.ln == nil {
		return nil
	}

	var (
		err   error
		didDo bool
	)

	s.onceStop.Do(func() {
		closeOrSkip(s.stopping)

		err = s.ln.Close()
		didDo = true
	})

	if !didDo && err == nil {
		return ErrSrvClosed
	}

	tc := time.NewTicker(shutdownLoopDelay)
	defer tc.Stop()

	for {
		if s.closeIdle() && s.isStopping() {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tc.C:
		}
	}

	s.wg.Wait()

	return err
}

// Started returns true if the server is started.
func (s *Server) Started() bool {
	return s.isStarted()
}

// preStart checks if Server can be started.
//
// It must be called only once inside Start.
// Subsequent calls return ErrSrvStarted.
func (s *Server) preStart() error {
	if s.cfg == nil {
		return ErrInvalidServerOptions
	}

	if s.addr == nil {
		return ErrInvalidAddr
	}

	if s.ln == nil {
		return ErrInvalidListener
	}

	if err := s.cfg.check(); err != nil {
		return err
	}

	if s.isStopping() || s.isClosed() {
		return ErrSrvClosed
	}

	if s.isStarted() {
		return ErrSrvStarted
	}

	return nil
}

func (s *Server) isStarted() bool {
	select {
	case <-s.started:
		return true
	default:
		return false
	}
}

func (s *Server) isStopping() bool {
	select {
	case <-s.stopping:
		return true
	default:
		return false
	}
}

func (s *Server) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *Server) addConn(c *conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) delConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// closeIdle closes new and idle connections and returns true if no connections left.
//
// A successful call is broadcasted by closing s.closed.
func (s *Server) closeIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	empty := true
	for c := range s.conns {
		if c == nil {
			continue
		}

		st := c.getState()
		if st != stateNew && st != stateIdle {
			empty = false
			continue
		}

		c.close()
		delete(s.conns, c)
	}

	if empty {
		s.onceClose.Do(func() { closeOrSkip(s.closed) })
	}

	return empty
}

func (s *Server) handle(pctx context.Context, c *conn, enc serverEncoder) {
	defer s.wg.Done()

	defer func() {
		c.close()

		s.retainBufReader(enc.reader())
		s.retainBufWriter(enc.writer())

		c.toClosed()
		s.delConn(c)
	}()

	wg := &sync.WaitGroup{}

	ctx, cancel := context.WithCancel(pctx)
	defer cancel()

	defer wg.Wait()

	for {
		select {
		case <-s.stopping:
			return
		case <-s.closed:
			return
		case <-ctx.Done():
			return
		default:
			var (
				req = s.reqList.obtainReq()
				err error
			)

			if err = enc.readRequest(ctx, req); err != nil {
				_ = s.shouldHangErr(err)
				cancel()

				s.reqList.retainReq(req)

				return
			}

			c.toActive()

			resp := s.respList.obtainResp()

			resp.Kind = req.Kind
			resp.Id = req.Id
			resp.Service = req.Service
			resp.Method = req.Method

			svc, err := s.getService(req)
			if err != nil {
				if err = sendRespErr(ctx, enc, resp, err); err != nil {
					if s.shouldHangErr(err) {
						cancel()

						s.reqList.retainReq(req)
						s.respList.retainResp(resp)

						return
					}
				}

				s.reqList.retainReq(req)
				s.respList.retainResp(resp)

				c.toIdleIfNoReqs()

				continue
			}

			c.addReq()

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.toIdleIfNoReqs()
				defer c.doneReq()

				if err := svc.do(ctx, enc, req, resp); err != nil {
					if s.shouldHangErr(err) {
						cancel()

						s.reqList.retainReq(req)
						s.respList.retainResp(resp)

						return
					}
				}

				s.reqList.retainReq(req)
				s.respList.retainResp(resp)
			}()

			c.toIdleIfNoReqs()
		}
	}
}

func (s *Server) getService(req *wire.Request) (*service, error) {
	svcn := req.GetService()
	if svcn == "" {
		return nil, errInvalidReqServiceName
	}

	s.smu.Lock()
	svc, ok := s.sset[svcn]
	s.smu.Unlock()

	if !ok {
		return nil, errUnknownReqService
	}

	return svc, nil
}

func (s *Server) shouldHangErr(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	return false
}

func (s *Server) obtainBufReader(r io.Reader) bufReader {
	raw := s.rbufPool.Get()
	if raw == nil {
		return newBufReader(r, s.cfg.ReadBufSize)
	}

	result, ok := raw.(bufReader)
	if !ok {
		return newBufReader(r, s.cfg.ReadBufSize)
	}

	result.Reset(r)

	return result
}

func (s *Server) retainBufReader(br bufReader) {
	br.Reset(nil)

	s.rbufPool.Put(br)
}

func (s *Server) obtainBufWriter(w io.Writer) bufWriter {
	raw := s.wbufPool.Get()
	if raw == nil {
		return newBufWriter(w, s.cfg.WriteBufSize)
	}

	result, ok := raw.(bufWriter)
	if !ok {
		return newBufWriter(w, s.cfg.WriteBufSize)
	}

	result.Reset(w)

	return result
}

func (s *Server) retainBufWriter(bw bufWriter) {
	bw.Reset(nil)

	s.wbufPool.Put(bw)
}

// ---

const (
	stateNew cstate = iota
	stateActive
	stateIdle
	stateClosed
)

type cstate uint32

type conn struct {
	raddr string
	state struct{ value uint32 }

	// Protects only nreq.
	mu   *sync.Mutex
	nreq uint64

	rwc io.ReadWriteCloser
}

func newConn(raddr string, rwc io.ReadWriteCloser) *conn {
	c := &conn{
		raddr: raddr,
		mu:    &sync.Mutex{},
		rwc:   rwc,
	}

	return c
}

func (c *conn) close() error {
	return c.rwc.Close()
}

func (c *conn) toNew() {
	c.setState(stateNew)
}

func (c *conn) toActive() {
	c.setState(stateActive)
}

func (c *conn) toIdle() {
	c.setState(stateIdle)
}

func (c *conn) toIdleIfNoReqs() {
	if c.hasNoReqs() {
		c.toIdle()
	}
}

func (c *conn) toClosed() {
	c.setState(stateClosed)
}

func (c *conn) getState() cstate {
	return cstate(atomic.LoadUint32(&c.state.value))
}

func (c *conn) setState(s cstate) {
	atomic.StoreUint32(&c.state.value, uint32(s))
}

func (c *conn) addReq() {
	c.mu.Lock()
	c.nreq += 1
	c.mu.Unlock()
}

func (c *conn) doneReq() {
	c.mu.Lock()
	if c.nreq > 0 {
		c.nreq -= 1
	}
	c.mu.Unlock()
}

func (c *conn) resetReq() {
	c.mu.Lock()
	c.nreq = 0
	c.mu.Unlock()
}

func (c *conn) hasNoReqs() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.nreq == 0
}

// Service.

type service struct {
	name string

	recvt reflect.Type
	recvv reflect.Value

	mu   *sync.Mutex
	mset map[string]*method
}

func newService(raw interface{}) (*service, error) {
	recvt := reflect.TypeOf(raw)
	if recvt.Kind() != reflect.Ptr {
		return nil, errInvalidServiceType
	}

	recvv := reflect.ValueOf(raw)
	name := reflect.Indirect(recvv).Type().Name()

	if name == "" {
		return nil, errInvalidServiceName
	}

	if !token.IsExported(name) {
		return nil, errUnexportedService
	}

	mtds := createMethods(recvt, recvv)
	if len(mtds) == 0 {
		return nil, errInvalidServiceMethods
	}

	result := &service{
		name:  name,
		recvt: recvt,
		recvv: recvv,
		mu:    &sync.Mutex{},
		mset:  mtds,
	}

	return result, nil
}

func (s *service) do(ctx context.Context, enc serverEncoder, req *wire.Request, resp *wire.Response) error {
	mtd, err := s.getMethod(req)
	if err != nil {
		if err = sendRespErr(ctx, enc, resp, err); err != nil {
			return err
		}

		return nil
	}

	respval, err := mtd.do(ctx, req, resp)
	if err != nil {
		if err = sendRespErr(ctx, enc, resp, err); err != nil {
			return err
		}

		return nil
	}

	return enc.writeResponse(ctx, resp, respval)
}

func (s *service) getMethod(req *wire.Request) (*method, error) {
	mtn := req.GetMethod()
	if mtn == "" {
		return nil, errInvalidReqServiceMethod
	}

	s.mu.Lock()
	mtd, ok := s.mset[mtn]
	s.mu.Unlock()

	if !ok {
		return nil, errUnknownReqServiceMethod
	}

	return mtd, nil
}

type method struct {
	name  string
	recvv reflect.Value

	mtd   reflect.Method
	argt  reflect.Type
	respt reflect.Type
}

func (m *method) do(ctx context.Context, req *wire.Request, resp *wire.Response) (interface{}, error) {
	argv, err := m.parseArgs(req)
	if err != nil {
		return nil, err
	}

	respv := m.makeResp()
	retval := m.mtd.Func.Call([]reflect.Value{
		m.recvv,
		reflect.ValueOf(ctx),
		argv,
		respv,
	})

	m.checkRetval(resp, retval)

	var respval interface{}
	if resp.Error != "" {
		respval = &wire.InvalidRequest{}
	} else {
		respval = respv.Interface()
	}

	return respval, nil
}

func (m *method) parseArgs(req *wire.Request) (reflect.Value, error) {
	argv := reflect.New(m.argt.Elem())
	if err := unmarshal(Encoding(req.Kind), req.Data, argv.Interface()); err != nil {
		return reflect.Value{}, err
	}

	return argv, nil
}

func (m *method) makeResp() reflect.Value {
	respv := reflect.New(m.respt.Elem())
	switch m.respt.Elem().Kind() {
	case reflect.Map:
		respv.Elem().Set(reflect.MakeMap(m.respt.Elem()))
	case reflect.Slice:
		respv.Elem().Set(reflect.MakeSlice(m.respt.Elem(), 0, 0))
	}

	return respv
}

func (m *method) checkRetval(resp *wire.Response, retval []reflect.Value) {
	switch {
	case len(retval) == 1:
		v := retval[0].Interface()
		if v == nil {
			return
		}

		verr, ok := v.(error)
		if ok {
			resp.Error = verr.Error()
		} else {
			resp.Error = msgInvalidReturnType
		}
	default:
		resp.Error = msgInvalidReturnNum
	}
}

// ---

// backoff is a simplest backoff calculator.
//
// It is NOT safe for concurrent use.
// The counter calculates delay by multiplying start by factor.
// Once delay exceeds max, max is returned on every call to next().
// The value of start must not be greater than max.
// The value of delay can be reset by calling resetd().
type backoff struct {
	start  time.Duration
	max    time.Duration
	delay  time.Duration
	factor uint8
}

func newBackoff(start, max time.Duration, factor uint8) *backoff {
	result := &backoff{
		start:  start,
		max:    max,
		factor: factor,
	}

	return result
}

func newDefaultBackoff() *backoff {
	return newBackoff(10*time.Millisecond, 1*time.Second, 2)
}

func (b *backoff) next() time.Duration {
	if b == nil {
		return 0
	}

	if b.delay == b.max {
		return b.delay
	}

	if b.delay == 0 {
		b.delay = b.start
		return b.delay
	}

	b.delay *= time.Duration(b.factor)
	if b.delay > b.max {
		b.delay = b.max
	}

	return b.delay
}

func (b *backoff) resetd() {
	if b == nil {
		return
	}

	b.delay = 0
}

type requestRetainer chan *wire.Request

func newRequestRetainer(size int) requestRetainer {
	if size == 0 {
		size = runtime.NumCPU()
	}

	return make(requestRetainer, size)
}

func (r requestRetainer) obtainReq() *wire.Request {
	select {
	case req := <-r:
		return req
	default:
		return &wire.Request{}
	}
}

func (r requestRetainer) retainReq(req *wire.Request) {
	req.Reset()

	select {
	case r <- req:
	default:
	}
}

type responseRetainer chan *wire.Response

func newResponseRetainer(size int) responseRetainer {
	if size == 0 {
		size = runtime.NumCPU()
	}

	return make(responseRetainer, size)
}

func (r responseRetainer) obtainResp() *wire.Response {
	select {
	case resp := <-r:
		return resp
	default:
		return &wire.Response{}
	}
}

func (r responseRetainer) retainResp(resp *wire.Response) {
	resp.Reset()

	select {
	case r <- resp:
	default:
	}
}

func checkEncoding(kind Encoding) bool {
	switch kind {
	case Protobuf:
		return true
	case JSON:
		return true
	default:
		return false
	}
}

var (
	tCtxArg = reflect.TypeOf((*context.Context)(nil)).Elem()
	terrArg = reflect.TypeOf((*error)(nil)).Elem()
)

func createMethods(svct reflect.Type, svcv reflect.Value) map[string]*method {
	result := make(map[string]*method)

	for n, max := 0, svct.NumMethod(); n < max; n++ {
		mtd := svct.Method(n)

		if !token.IsExported(mtd.Name) {
			continue
		}

		if mtd.Type.NumIn() != 4 {
			continue
		}

		if mtd.Type.NumOut() != 1 {
			continue
		}

		if tctx := mtd.Type.In(1); tctx != tCtxArg {
			continue
		}

		argt := mtd.Type.In(2)
		if argt.Kind() != reflect.Ptr || !isUsableType(argt) {
			continue
		}

		respt := mtd.Type.In(3)
		if respt.Kind() != reflect.Ptr || !isUsableType(respt) {
			continue
		}

		if rtp := mtd.Type.Out(0); rtp != terrArg {
			continue
		}

		result[mtd.Name] = &method{
			name:  mtd.Name,
			recvv: svcv,
			mtd:   mtd,
			argt:  argt,
			respt: respt,
		}
	}

	return result
}

func isUsableType(tp reflect.Type) bool {
	for tp.Kind() == reflect.Ptr {
		tp = tp.Elem()
	}

	return token.IsExported(tp.Name()) || tp.PkgPath() == ""
}

func sendRespErr(ctx context.Context, enc serverEncoder, resp *wire.Response, rerr error) error {
	if rerr != nil {
		resp.Error = rerr.Error()
	} else {
		resp.Error = msgUnexpectedError
	}

	return enc.writeResponse(ctx, resp, &wire.InvalidRequest{})
}

func closeOrSkip(c chan struct{}) {
	select {
	case <-c:
	default:
		close(c)
	}
}

func calcBufSize(x int) int {
	if x < 0 {
		return x
	}

	if x >= 0 && x < bufSize8k {
		return defBufSize
	}

	if x > bufSize256k {
		return bufSize512k
	}

	return nextPow2(x)
}

// nextPow2 returns a number which is the next power of 2 larger than x.
//
// The algorithm was found here http://graphics.stanford.edu/~seander/bithacks.html#RoundUpPowerOf2.
func nextPow2(x int) int {
	// Special case. Don't care about negatives.
	if x < 0 {
		return 1
	}

	if x == 0 {
		return 1
	}

	x--
	x |= x >> 1
	x |= x >> 2
	x |= x >> 4
	x |= x >> 8
	x |= x >> 16
	x++

	return x
}
