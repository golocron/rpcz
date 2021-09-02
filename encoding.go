package rpcz

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"

	"github.com/golocron/rpcz/internal/wire"
)

type serverEncoder interface {
	readRequest(ctx context.Context, req *wire.Request) error
	writeResponse(ctx context.Context, resp *wire.Response, v interface{}) error

	reader() bufReader
	writer() bufWriter
}

type clientEncoder interface {
	readResponse(ctx context.Context, resp *wire.Response) error
	writeRequest(ctx context.Context, req *wire.Request, v interface{}) error

	reader() bufReader
	writer() bufWriter
}

func newServerEncoder(kind Encoding, r bufReader, w bufWriter) serverEncoder {
	if kind == JSON {
		return newSrvEncoderJSON(kind, r, w)
	}

	return newSrvEncoderPbf(kind, r, w)
}

func newClientEncoder(kind Encoding, r bufReader, w bufWriter) clientEncoder {
	if kind == JSON {
		return newClientEncoderJSON(kind, r, w)
	}

	return newClientEncoderPbf(kind, r, w)
}

func newBufReader(rd io.Reader, size int) bufReader {
	if size < 0 {
		return newNoBufReader(rd)
	}

	return bufio.NewReaderSize(rd, size)
}

func newBufWriter(w io.Writer, size int) bufWriter {
	if size < 0 {
		return newNoBufWriter(w)
	}

	return bufio.NewWriterSize(w, size)
}

type bufReader interface {
	io.Reader
	io.ByteReader
	peekDiscarder
	Buffered() int
	Reset(r io.Reader)
}

type bufWriter interface {
	io.Writer
	flusher
	Reset(w io.Writer)
}

type peekDiscarder interface {
	Peek(n int) ([]byte, error)
	Discard(n int) (int, error)
}

type flusher interface {
	Flush() error
}

// Protobuf encoding.

type srvEncoderPbf struct {
	kind Encoding
	rmu  *sync.Mutex
	wmu  *sync.Mutex
	d    *encDriverPbf
}

func newSrvEncoderPbf(kind Encoding, r bufReader, w bufWriter) *srvEncoderPbf {
	result := &srvEncoderPbf{
		kind: kind,
		rmu:  &sync.Mutex{},
		wmu:  &sync.Mutex{},
		d:    newEncDriverPbf(r, w),
	}

	return result
}

func (c *srvEncoderPbf) readRequest(ctx context.Context, req *wire.Request) error {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	return c.d.decode(ctx, req)
}

func (c *srvEncoderPbf) writeResponse(ctx context.Context, resp *wire.Response, v interface{}) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return errInvalidProtobufMsg
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	resp.Data = data
	raw, err := proto.Marshal(resp)
	if err != nil {
		return err
	}

	c.wmu.Lock()
	err = c.d.encode(ctx, raw)
	c.wmu.Unlock()

	return err
}

func (c *srvEncoderPbf) reader() bufReader {
	return c.d.r
}

func (c *srvEncoderPbf) writer() bufWriter {
	return c.d.w
}

type clientEncoderPbf struct {
	kind Encoding
	rmu  *sync.Mutex
	wmu  *sync.Mutex
	d    *encDriverPbf
}

func newClientEncoderPbf(kind Encoding, r bufReader, w bufWriter) *clientEncoderPbf {
	result := &clientEncoderPbf{
		kind: kind,
		rmu:  &sync.Mutex{},
		wmu:  &sync.Mutex{},
		d:    newEncDriverPbf(r, w),
	}

	return result
}

func (c *clientEncoderPbf) readResponse(ctx context.Context, resp *wire.Response) error {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	return c.d.decode(ctx, resp)
}

func (c *clientEncoderPbf) writeRequest(ctx context.Context, req *wire.Request, v interface{}) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return errInvalidProtobufMsg
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	req.Data = data
	raw, err := proto.Marshal(req)
	if err != nil {
		return err
	}

	c.wmu.Lock()
	err = c.d.encode(ctx, raw)
	c.wmu.Unlock()

	return err
}

func (c *clientEncoderPbf) reader() bufReader {
	return c.d.r
}

func (c *clientEncoderPbf) writer() bufWriter {
	return c.d.w
}

type encDriverPbf struct {
	r bufReader
	w bufWriter
}

func newEncDriverPbf(r bufReader, w bufWriter) *encDriverPbf {
	result := &encDriverPbf{r: r, w: w}

	return result
}

func (d *encDriverPbf) decode(ctx context.Context, dst proto.Message) error {
	size, err := binary.ReadUvarint(d.r)
	if err != nil {
		return err
	}

	if size == 0 {
		return nil
	}

	isize := int(size)

	if d.r.Buffered() >= isize {
		data, err := d.r.Peek(isize)
		if err != nil {
			return err
		}

		err = proto.Unmarshal(data, dst)

		_, derr := d.r.Discard(isize)
		if err == nil {
			return derr
		}

		return err
	}

	data := make([]byte, isize)
	if _, err := io.ReadFull(d.r, data); err != nil {
		return err
	}

	return proto.Unmarshal(data, dst)
}

func (d *encDriverPbf) encode(ctx context.Context, parts ...[]byte) error {
	for _, part := range parts {
		if err := d.encodePart(ctx, part); err != nil {
			_ = d.w.Flush()

			return err
		}
	}

	return d.w.Flush()
}

func (d *encDriverPbf) encodePart(ctx context.Context, data []byte) error {
	var bin [binary.MaxVarintLen64]byte
	head := bin[:]

	if len(data) == 0 {
		hlen := binary.PutUvarint(head, uint64(0))
		if err := d.write(ctx, head[:hlen]); err != nil {
			return err
		}

		return nil
	}

	hlen := binary.PutUvarint(head, uint64(len(data)))
	if err := d.write(ctx, head[:hlen]); err != nil {
		return err
	}

	if err := d.write(ctx, data); err != nil {
		return err
	}

	return nil
}

func (d *encDriverPbf) write(ctx context.Context, data []byte) error {
	boff := newDefaultBackoff()

	for i, size := 0, len(data); i < size; {
		n, err := d.w.Write(data[i:])
		if err != nil {
			nerr, ok := err.(net.Error)
			if !ok || !nerr.Temporary() {
				return err
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(boff.next()):
			}
		}

		if err == nil {
			boff.resetd()
		}

		i += n
	}

	return nil
}

// JSON encoding.

type srequestJSON struct {
	Kind    Encoding         `json:"kind"`
	ID      uint64           `json:"id"`
	Service string           `json:"service"`
	Method  string           `json:"method"`
	Data    *json.RawMessage `json:"data"`
}

type sresponseJSON struct {
	Kind    Encoding    `json:"kind"`
	ID      uint64      `json:"id"`
	Service string      `json:"service"`
	Method  string      `json:"method"`
	Error   string      `json:"error"`
	Data    interface{} `json:"data"`
}

type srvEncoderJSON struct {
	kind Encoding
	rmu  *sync.Mutex
	wmu  *sync.Mutex
	d    *encDriverJSON
}

func newSrvEncoderJSON(kind Encoding, r bufReader, w bufWriter) *srvEncoderJSON {
	result := &srvEncoderJSON{
		kind: kind,
		rmu:  &sync.Mutex{},
		wmu:  &sync.Mutex{},
		d:    newEncDriverJSON(r, w),
	}

	return result
}

func (c *srvEncoderJSON) readRequest(ctx context.Context, req *wire.Request) error {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	jreq := &srequestJSON{}
	if err := c.d.decode(ctx, jreq); err != nil {
		return err
	}

	req.Kind = int32(jreq.Kind)
	req.Id = jreq.ID
	req.Service = jreq.Service
	req.Method = jreq.Method

	if jreq.Data != nil {
		req.Data = *jreq.Data
	}

	return nil
}

func (c *srvEncoderJSON) writeResponse(ctx context.Context, resp *wire.Response, v interface{}) error {
	jresp := &sresponseJSON{
		Kind:    Encoding(resp.Kind),
		ID:      resp.Id,
		Service: resp.Service,
		Method:  resp.Method,
		Error:   resp.Error,
		Data:    v,
	}

	c.wmu.Lock()
	err := c.d.encode(ctx, jresp)
	c.wmu.Unlock()

	return err
}

func (c *srvEncoderJSON) reader() bufReader {
	return c.d.r
}

func (c *srvEncoderJSON) writer() bufWriter {
	return c.d.w
}

type crequestJSON struct {
	Kind    Encoding    `json:"kind"`
	ID      uint64      `json:"id"`
	Service string      `json:"service"`
	Method  string      `json:"method"`
	Data    interface{} `json:"data"`
}

type cresponseJSON struct {
	Kind    Encoding         `json:"kind"`
	ID      uint64           `json:"id"`
	Service string           `json:"service"`
	Method  string           `json:"method"`
	Error   string           `json:"error"`
	Data    *json.RawMessage `json:"data"`
}

type clientEncoderJSON struct {
	kind Encoding
	rmu  *sync.Mutex
	wmu  *sync.Mutex
	d    *encDriverJSON
}

func newClientEncoderJSON(kind Encoding, r bufReader, w bufWriter) *clientEncoderJSON {
	result := &clientEncoderJSON{
		kind: kind,
		rmu:  &sync.Mutex{},
		wmu:  &sync.Mutex{},
		d:    newEncDriverJSON(r, w),
	}

	return result
}

func (c *clientEncoderJSON) readResponse(ctx context.Context, resp *wire.Response) error {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	jresp := &cresponseJSON{}
	if err := c.d.decode(ctx, jresp); err != nil {
		return err
	}

	resp.Kind = int32(jresp.Kind)
	resp.Id = jresp.ID
	resp.Service = jresp.Service
	resp.Method = jresp.Method
	resp.Error = jresp.Error

	if jresp.Data != nil {
		resp.Data = *jresp.Data
	}

	return nil
}

func (c *clientEncoderJSON) writeRequest(ctx context.Context, req *wire.Request, v interface{}) error {
	jreq := &crequestJSON{
		Kind:    Encoding(req.Kind),
		ID:      req.Id,
		Service: req.Service,
		Method:  req.Method,
		Data:    v,
	}

	c.wmu.Lock()
	err := c.d.encode(ctx, jreq)
	c.wmu.Unlock()

	return err
}

func (c *clientEncoderJSON) reader() bufReader {
	return c.d.r
}

func (c *clientEncoderJSON) writer() bufWriter {
	return c.d.w
}

type encDriverJSON struct {
	r bufReader
	w bufWriter

	d *json.Decoder
	e *json.Encoder
}

func newEncDriverJSON(r bufReader, w bufWriter) *encDriverJSON {
	result := &encDriverJSON{
		r: r,
		w: w,

		d: json.NewDecoder(r),
		e: json.NewEncoder(w),
	}

	return result
}

func (d *encDriverJSON) decode(ctx context.Context, v interface{}) error {
	return d.d.Decode(v)
}

func (d *encDriverJSON) encode(ctx context.Context, parts ...interface{}) error {
	for _, part := range parts {
		if err := d.encodePart(ctx, part); err != nil {
			_ = d.w.Flush()

			return err
		}
	}

	return d.w.Flush()
}

func (d *encDriverJSON) encodePart(ctx context.Context, data interface{}) error {
	return d.e.Encode(data)
}

type noBufReader struct {
	r io.Reader
}

func newNoBufReader(r io.Reader) *noBufReader {
	return &noBufReader{r: r}
}

// Read delegates the call directly to the underlying r.
func (r *noBufReader) Read(p []byte) (int, error) {
	return r.r.Read(p)
}

// ReadByte reads exactly one byte using Read.
func (r *noBufReader) ReadByte() (byte, error) {
	var one [1]byte
	b := one[:]

	n, err := r.r.Read(b)
	if err != nil {
		return 0, err
	}

	if n == 0 {
		return 0, io.EOF
	}

	return b[0], nil
}

// Buffered is a no-op.
//
// It returns -1 to always be less than the size of a message.
func (r *noBufReader) Buffered() int {
	return -1
}

// Peek returns nil and no error.
//
// Calls to Peek are usually preceeded by calling Buffered().
// Since Buffered() always returns -1, this method must not be called.
func (r *noBufReader) Peek(n int) ([]byte, error) {
	return nil, nil
}

// Discard returns 0 and no error.
func (r *noBufReader) Discard(n int) (int, error) {
	return 0, nil
}

// Reset sets the underlying r to rd.
func (r *noBufReader) Reset(rd io.Reader) {
	r.r = rd
}

type noBufWriter struct {
	w io.Writer
}

func newNoBufWriter(w io.Writer) *noBufWriter {
	return &noBufWriter{w: w}
}

// Write delegates the call directly to the underlying w.
func (w *noBufWriter) Write(p []byte) (int, error) {
	return w.w.Write(p)
}

// Flush is a no-op.
func (w *noBufWriter) Flush() error {
	return nil
}

// Reset sets the underlying w to wt.
func (w *noBufWriter) Reset(wt io.Writer) {
	w.w = wt
}

func marshal(kind Encoding, v interface{}) ([]byte, error) {
	switch kind {
	case Protobuf:
		return marshalPbf(v)
	case JSON:
		return json.Marshal(v)
	default:
		return nil, errInvalidEncoding
	}
}

func unmarshal(kind Encoding, data []byte, v interface{}) error {
	switch kind {
	case Protobuf:
		return unmarshalPbf(data, v)
	case JSON:
		return json.Unmarshal(data, v)
	default:
		return errInvalidEncoding
	}
}

func marshalPbf(v interface{}) ([]byte, error) {
	if v == nil {
		return nil, errInvalidProtobufMsg
	}

	m, ok := v.(proto.Message)
	if !ok {
		return nil, errInvalidProtobufMsg
	}

	return proto.Marshal(m)
}

func unmarshalPbf(data []byte, v interface{}) error {
	m, ok := v.(proto.Message)
	if !ok {
		return errInvalidProtobufMsg
	}

	return proto.Unmarshal(data, m)
}
