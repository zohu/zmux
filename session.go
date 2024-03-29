package zmux

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	goAwayNormal uint32 = iota
	goAwayProtoErr
	goAwayInternalErr
)

type Session struct {
	conf    *Config
	conn    io.ReadWriteCloser
	bufRead *bufio.Reader

	localGoAway  int32
	remoteGoAway int32

	streams        map[uint32]*Stream
	streamInflight map[uint32]struct{}
	streamLock     sync.Mutex

	syncCh   chan struct{}
	acceptCh chan *Stream

	next uint32

	sendCh chan *sendReady

	sendDone chan struct{}
	recvDone chan struct{}

	pings    map[uint32]chan struct{}
	pingID   uint32
	pingLock sync.Mutex

	shutdown        bool
	shutdownCh      chan struct{}
	shutdownLock    sync.Mutex
	shutdownErr     error
	shutdownErrLock sync.Mutex
}
type sendReady struct {
	Head header
	Body []byte
	Ech  chan error
	l    sync.Mutex
}

func newSession(conf *Config, conn io.ReadWriteCloser, isClient bool) *Session {
	s := &Session{
		conf:           conf,
		conn:           conn,
		bufRead:        bufio.NewReader(conn),
		streams:        make(map[uint32]*Stream),
		streamInflight: make(map[uint32]struct{}),
		syncCh:         make(chan struct{}, conf.MaxStream),
		acceptCh:       make(chan *Stream, conf.MaxStream),
		sendCh:         make(chan *sendReady, 64),
		sendDone:       make(chan struct{}),
		recvDone:       make(chan struct{}),
		pings:          make(map[uint32]chan struct{}),
		shutdownCh:     make(chan struct{}),
	}
	if isClient {
		s.next = 1
	} else {
		s.next = 2
	}
	go s.recv()
	go s.send()
	if Val(conf.KeepAlive) {
		go s.keepalive()
	}
	return s
}
func (s *Session) Addr() net.Addr {
	return s.LocalAddr()
}
func (s *Session) LocalAddr() net.Addr {
	addr, ok := s.conn.(lrAddr)
	if !ok {
		return &zmuxAddr{"local"}
	}
	return addr.LocalAddr()
}
func (s *Session) RemoteAddr() net.Addr {
	addr, ok := s.conn.(lrAddr)
	if !ok {
		return &zmuxAddr{"remote"}
	}
	return addr.RemoteAddr()
}
func (s *Session) Ping() (time.Duration, error) {
	ch := make(chan struct{})
	s.pingLock.Lock()
	id := s.pingID
	s.pingID++
	s.pings[id] = ch
	s.pingLock.Unlock()

	hd := newHeader()
	hd.encode(msgTypePing, flagSYN, 0, id)
	if err := s.waitSend(hd, nil); err != nil {
		return 0, err
	}
	start := time.Now()
	select {
	case <-ch:
	case <-time.After(s.conf.TimeoutWrite):
		s.pingLock.Lock()
		delete(s.pings, id)
		s.pingLock.Unlock()
		return 0, ErrTimeout
	case <-s.shutdownCh:
		return 0, ErrSessionShutdown
	}
	return time.Now().Sub(start), nil
}
func (s *Session) IsClosed() bool {
	select {
	case <-s.shutdownCh:
		return true
	default:
		return false
	}
}
func (s *Session) Open() (net.Conn, error) {
	conn, err := s.OpenStream()
	if err != nil {
		return nil, err
	}
	return conn, nil
}
func (s *Session) OpenStream() (*Stream, error) {
	if s.IsClosed() {
		return nil, ErrSessionShutdown
	}
	if atomic.LoadInt32(&s.remoteGoAway) == 1 {
		return nil, ErrRemoteGoAway
	}

	select {
	case s.syncCh <- struct{}{}:
	case <-s.shutdownCh:
		return nil, ErrSessionShutdown
	}

GetId:
	id := atomic.LoadUint32(&s.next)
	if id >= math.MaxUint32-1 {
		return nil, ErrStreamsExhausted
	}
	if !atomic.CompareAndSwapUint32(&s.next, id, id+2) {
		goto GetId
	}

	stream := newStream(s, id, stateInit)
	s.streamLock.Lock()
	s.streams[id] = stream
	s.streamInflight[id] = struct{}{}
	s.streamLock.Unlock()
	if s.conf.TimeoutStreamOpen > 0 {
		go s.setOpenTimeout(stream)
	}
	if err := stream.sendUpdate(); err != nil {
		select {
		case <-s.syncCh:
		default:
			s.conf.LogWriter.Errorf("aborted stream open without inflight syn semaphore")
		}
		return nil, err
	}
	return stream, nil
}
func (s *Session) Accept() (net.Conn, error) {
	conn, err := s.AcceptStream()
	if err != nil {
		return nil, err
	}
	return conn, err
}
func (s *Session) AcceptStream() (*Stream, error) {
	select {
	case stream := <-s.acceptCh:
		if err := stream.sendUpdate(); err != nil {
			return nil, err
		}
		return stream, nil
	case <-s.shutdownCh:
		return nil, s.shutdownErr
	}
}
func (s *Session) AcceptStreamWithContext(ctx context.Context) (*Stream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case stream := <-s.acceptCh:
		if err := stream.sendUpdate(); err != nil {
			return nil, err
		}
		return stream, nil
	case <-s.shutdownCh:
		return nil, s.shutdownErr
	}
}
func (s *Session) GoAway() error {
	return s.waitSend(s.goAway(goAwayNormal), nil)
}
func (s *Session) NumStreams() int {
	s.streamLock.Lock()
	num := len(s.streams)
	s.streamLock.Unlock()
	return num
}
func (s *Session) Close() error {
	s.shutdownLock.Lock()
	defer s.shutdownLock.Unlock()
	if s.shutdown {
		return nil
	}
	s.shutdown = true
	s.shutdownErrLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = ErrSessionShutdown
	}
	s.shutdownErrLock.Unlock()
	close(s.shutdownCh)
	_ = s.conn.Close()
	<-s.recvDone

	s.streamLock.Lock()
	defer s.streamLock.Unlock()
	for _, stm := range s.streams {
		stm.forceClose()
	}
	<-s.sendDone
	return nil
}
func (s *Session) CloseChan() <-chan struct{} {
	return s.shutdownCh
}

func (s *Session) send() {
	if err := s.sendLoop(); err != nil {
		s.exitErr(err)
	}
}
func (s *Session) sendLoop() error {
	defer close(s.sendDone)
	var bodyBuf bytes.Buffer
	for {
		bodyBuf.Reset()
		select {
		case ready := <-s.sendCh:
			if ready.Head != nil {
				_, err := s.conn.Write(ready.Head)
				if err != nil {
					s.conf.LogWriter.Errorf("failed to write header: %v", err)
					sendErr(ready.Ech, err)
					return err
				}
			}
			ready.l.Lock()
			if ready.Body != nil {
				_, err := bodyBuf.Write(ready.Body)
				if err != nil {
					ready.Body = nil
					ready.l.Unlock()
					s.conf.LogWriter.Errorf("failed to copy body into buffer: %v", err)
					sendErr(ready.Ech, err)
					return err
				}
				ready.Body = nil
			}
			ready.l.Unlock()
			if bodyBuf.Len() > 0 {
				_, err := s.conn.Write(bodyBuf.Bytes())
				if err != nil {
					s.conf.LogWriter.Errorf("failed to write body: %v", err)
					sendErr(ready.Ech, err)
					return err
				}
			}
			sendErr(ready.Ech, nil)
		case <-s.shutdownCh:
			return nil
		}
	}
}
func (s *Session) recv() {
	if err := s.recvLoop(); err != nil {
		s.exitErr(err)
	}
}
func (s *Session) recvLoop() error {
	defer close(s.recvDone)
	hd := newHeader()
	for {
		if _, err := io.ReadFull(s.bufRead, hd); err != nil {
			if err != io.EOF &&
				!strings.Contains(err.Error(), "closed") &&
				!strings.Contains(err.Error(), "reset by peer") {
				s.conf.LogWriter.Errorf("read header failed: %v", err)
			}
			return err
		}
		if hd.Version() != headVersion {
			s.conf.LogWriter.Errorf("unsupported version: %d", hd.Version())
			return ErrInvalidVersion
		}
		mt := hd.MsgType()
		if mt < msgTypeData || mt > msgTypeGoAway {
			return ErrInvalidMsgType
		}
		if err := handlers[mt](s, hd); err != nil {
			return err
		}
	}
}
func (s *Session) keepalive() {
	for {
		select {
		case <-time.After(s.conf.KeepAliveInterval):
			_, err := s.Ping()
			if err != nil {
				if !errors.Is(err, ErrSessionShutdown) {
					s.conf.LogWriter.Errorf("keepalive failed: %v", err)
					s.exitErr(ErrKeepAliveTimeout)
				}
				return
			}
		case <-s.shutdownCh:
			return
		}
	}
}

var handlers = []func(*Session, header) error{
	msgTypeData:   (*Session).handleStreamMessage,
	msgTypeUpdate: (*Session).handleStreamMessage,
	msgTypePing:   (*Session).handlePing,
	msgTypeGoAway: (*Session).handleGoAway,
}

func (s *Session) handleStreamMessage(hdr header) error {
	id := hdr.StreamID()
	flags := hdr.Flags()
	if flags&flagSYN == flagSYN {
		if err := s.incomingStream(id); err != nil {
			return err
		}
	}

	s.streamLock.Lock()
	stream := s.streams[id]
	s.streamLock.Unlock()

	if stream == nil {
		if hdr.MsgType() == msgTypeData && hdr.Length() > 0 {
			s.conf.LogWriter.Errorf("discarding data for stream: %d", id)
			if _, err := io.CopyN(io.Discard, s.bufRead, int64(hdr.Length())); err != nil {
				s.conf.LogWriter.Errorf("failed to discard data: %v", err)
				return nil
			}
		} else {
			s.conf.LogWriter.Errorf("frame for missing stream: %v", hdr)
		}
		return nil
	}

	if hdr.MsgType() == msgTypeUpdate {
		if err := stream.incrSendWindow(hdr, flags); err != nil {
			if err := s.sendWithoutWait(s.goAway(goAwayProtoErr)); err != nil {
				s.conf.LogWriter.Errorf("failed to send go away: %v", err)
			}
			return err
		}
		return nil
	}

	if err := stream.readData(hdr, flags, s.bufRead); err != nil {
		if err := s.sendWithoutWait(s.goAway(goAwayProtoErr)); err != nil {
			s.conf.LogWriter.Errorf("failed to send go away: %v", err)
		}
		return err
	}
	return nil
}
func (s *Session) handlePing(hdr header) error {
	flags := hdr.Flags()
	pingID := hdr.Length()
	if flags&flagSYN == flagSYN {
		go func() {
			hdr := newHeader()
			hdr.encode(msgTypePing, flagACK, 0, pingID)
			if err := s.sendWithoutWait(hdr); err != nil {
				s.conf.LogWriter.Errorf("failed to send ping reply: %v", err)
			}
		}()
		return nil
	}

	s.pingLock.Lock()
	ch := s.pings[pingID]
	if ch != nil {
		delete(s.pings, pingID)
		close(ch)
	}
	s.pingLock.Unlock()
	return nil
}
func (s *Session) handleGoAway(hdr header) error {
	code := hdr.Length()
	switch code {
	case goAwayNormal:
		atomic.SwapInt32(&s.remoteGoAway, 1)
	case goAwayProtoErr:
		s.conf.LogWriter.Errorf("received protocol error go away")
		return fmt.Errorf("zmux protocol error")
	case goAwayInternalErr:
		s.conf.LogWriter.Errorf("received internal error go away")
		return fmt.Errorf("remote zmux internal error")
	default:
		s.conf.LogWriter.Errorf("received unexpected go away code: %d", code)
		return fmt.Errorf("unexpected go away received")
	}
	return nil
}

func (s *Session) waitSend(hdr header, body []byte) error {
	errCh := make(chan error, 1)
	return s.waitSendErr(hdr, body, errCh)
}
func (s *Session) waitSendErr(hd header, body []byte, ech chan error) error {
	timer := timerPool.Get().(*time.Timer)
	timer.Reset(s.conf.TimeoutWrite)
	defer func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timerPool.Put(timer)
	}()
	ready := &sendReady{
		Head: hd,
		Body: body,
		Ech:  ech,
	}
	select {
	case s.sendCh <- ready:
	case <-s.shutdownCh:
		return ErrSessionShutdown
	case <-timer.C:
		return ErrTimeout
	}

	bopy := func() {
		if body == nil {
			return
		}
		ready.l.Lock()
		defer ready.l.Unlock()
		if ready.Body == nil {
			return
		}
		newBody := make([]byte, len(body))
		copy(newBody, body)
		ready.Body = newBody
	}
	select {
	case err := <-ech:
		return err
	case <-s.shutdownCh:
		bopy()
		return ErrSessionShutdown
	case <-timer.C:
		bopy()
		return ErrTimeout
	}
}
func (s *Session) closeStream(id uint32) {
	s.streamLock.Lock()
	defer s.streamLock.Unlock()
	if _, ok := s.streamInflight[id]; ok {
		select {
		case <-s.syncCh:
		default:
			s.conf.LogWriter.Errorf("stream %d not in inflight", id)
		}
	}
	delete(s.streams, id)
}
func (s *Session) sendWithoutWait(hd header) error {
	timer := timerPool.Get().(*time.Timer)
	timer.Reset(s.conf.TimeoutWrite)
	defer func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
		timerPool.Put(timer)
	}()
	select {
	case s.sendCh <- &sendReady{Head: hd}:
		return nil
	case <-s.shutdownCh:
		return ErrSessionShutdown
	case <-timer.C:
		return ErrTimeout
	}
}
func (s *Session) incomingStream(id uint32) error {
	if atomic.LoadInt32(&s.localGoAway) == 1 {
		hdr := newHeader()
		hdr.encode(msgTypeUpdate, flagRST, id, 0)
		return s.sendWithoutWait(hdr)
	}
	stream := newStream(s, id, stateRecv)

	s.streamLock.Lock()
	defer s.streamLock.Unlock()
	if _, ok := s.streams[id]; ok {
		s.conf.LogWriter.Errorf("duplicate stream declared")
		if err := s.sendWithoutWait(s.goAway(goAwayProtoErr)); err != nil {
			s.conf.LogWriter.Errorf("failed to send go away: %v", err)
		}
		return ErrDuplicateStream
	}

	s.streams[id] = stream

	select {
	case s.acceptCh <- stream:
		return nil
	default:
		s.conf.LogWriter.Errorf("backlog exceeded, forcing connection reset")
		delete(s.streams, id)
		hdr := newHeader()
		hdr.encode(msgTypeUpdate, flagRST, id, 0)
		return s.sendWithoutWait(hdr)
	}
}
func (s *Session) establishStream(id uint32) {
	s.streamLock.Lock()
	if _, ok := s.streamInflight[id]; ok {
		delete(s.streamInflight, id)
	} else {
		s.conf.LogWriter.Errorf("established stream without inflight SYN (no tracking entry)")
	}
	select {
	case <-s.syncCh:
	default:
		s.conf.LogWriter.Errorf("established stream without inflight SYN (didn't have semaphore)")
	}
	s.streamLock.Unlock()
}
func (s *Session) goAway(reason uint32) header {
	atomic.SwapInt32(&s.localGoAway, 1)
	hdr := newHeader()
	hdr.encode(msgTypeGoAway, 0, 0, reason)
	return hdr
}
func (s *Session) exitErr(err error) {
	s.shutdownErrLock.Lock()
	if s.shutdownErr == nil {
		s.shutdownErr = err
	}
	s.shutdownErrLock.Unlock()
	_ = s.Close()
}
func (s *Session) setOpenTimeout(stream *Stream) {
	timer := time.NewTimer(s.conf.TimeoutStreamOpen)
	defer timer.Stop()

	select {
	case <-stream.establishNotify:
		return
	case <-s.shutdownCh:
		return
	case <-timer.C:
		s.conf.LogWriter.Errorf("aborted stream open (destination=%s): %v", s.RemoteAddr().String(), ErrTimeout)
		s.Close()
	}
}
