package zmux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type state int

const (
	stateInit state = iota
	stateOpen
	stateRecv
	stateEstablished
	stateLocalClose
	stateRemoteClose
	stateClosed
	stateReset
)

const (
	flagSYN = 1 << iota
	flagACK
	flagFIN
	flagRST
)

type Stream struct {
	id      uint32
	session *Session

	state     state
	stateLock sync.Mutex

	controlHead header
	controlErr  chan error
	controlLock sync.Mutex

	sendSize   uint32
	sendHead   header
	sendErr    chan error
	sendNotify chan struct{}
	sendLock   sync.Mutex

	recvSize   uint32
	recvBuff   *bytes.Buffer
	recvNotify chan struct{}
	recvLock   sync.Mutex

	deadlineRead  atomic.Value
	deadlineWrite atomic.Value

	establishNotify chan struct{}

	closeTimer *time.Timer
}

func newStream(session *Session, id uint32, stt state) *Stream {
	s := &Stream{
		id:              id,
		state:           stt,
		session:         session,
		controlHead:     header(make([]byte, headerSize)),
		controlErr:      make(chan error, 1),
		sendSize:        defaultMaxStreamSize,
		sendHead:        header(make([]byte, headerSize)),
		sendErr:         make(chan error, 1),
		sendNotify:      make(chan struct{}, 1),
		recvSize:        defaultMaxStreamSize,
		recvNotify:      make(chan struct{}, 1),
		establishNotify: make(chan struct{}, 1),
	}
	s.deadlineRead.Store(time.Time{})
	s.deadlineWrite.Store(time.Time{})
	return s
}
func (s *Stream) LocalAddr() net.Addr {
	return s.session.LocalAddr()
}
func (s *Stream) RemoteAddr() net.Addr {
	return s.session.RemoteAddr()
}
func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	if err := s.SetWriteDeadline(t); err != nil {
		return err
	}
	return nil
}
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.deadlineRead.Store(t)
	sendNotify(s.recvNotify)
	return nil
}
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.deadlineWrite.Store(t)
	sendNotify(s.sendNotify)
	return nil
}

func (s *Stream) Session() *Session {
	return s.session
}
func (s *Stream) ID() uint32 {
	return s.id
}
func (s *Stream) Read(d []byte) (n int, err error) {
	defer sendNotify(s.recvNotify)
BEGIN:
	s.stateLock.Lock()
	switch s.state {
	case stateLocalClose:
		fallthrough
	case stateRemoteClose:
		fallthrough
	case stateClosed:
		s.recvLock.Lock()
		if s.recvBuff == nil || s.recvBuff.Len() == 0 {
			s.recvLock.Unlock()
			s.stateLock.Unlock()
			return 0, io.EOF
		}
		s.recvLock.Unlock()
	case stateReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	default:
	}
	s.stateLock.Unlock()

	s.recvLock.Lock()
	if s.recvBuff == nil || s.recvBuff.Len() == 0 {
		s.recvLock.Unlock()
		goto WAIT
	}

	n, _ = s.recvBuff.Read(d)
	s.recvLock.Unlock()

	if err = s.sendUpdate(); errors.Is(err, ErrSessionShutdown) {
		err = nil
	}
	return
WAIT:
	var timeout <-chan time.Time
	var timer *time.Timer
	if deadline := s.deadlineRead.Load().(time.Time); !deadline.IsZero() {
		timer = time.NewTimer(deadline.Sub(time.Now()))
		timeout = timer.C
	}
	select {
	case <-s.recvNotify:
		if timer != nil {
			timer.Stop()
		}
		goto BEGIN
	case <-timeout:
		return 0, ErrTimeout
	}
}
func (s *Stream) Write(d []byte) (int, error) {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	total := 0
	for total < len(d) {
		n, err := s.write(d[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
func (s *Stream) write(d []byte) (int, error) {
	var flags uint16
	var maxSize uint32
	var body []byte
BEGIN:
	s.stateLock.Lock()
	switch s.state {
	case stateLocalClose:
		fallthrough
	case stateClosed:
		s.stateLock.Unlock()
		return 0, ErrStreamClosed
	case stateReset:
		s.stateLock.Unlock()
		return 0, ErrConnectionReset
	default:
	}
	s.stateLock.Unlock()

	size := atomic.LoadUint32(&s.sendSize)
	if size == 0 {
		goto WAIT
	}

	flags = s.sendFlags()

	maxSize = min(size, uint32(len(d)))
	body = d[:maxSize]

	s.sendHead.encode(msgTypeData, flags, s.id, maxSize)
	if err := s.session.waitSendErr(s.sendHead, body, s.sendErr); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrTimeout) {
			s.sendHead = make([]byte, headerSize)
		}
		return 0, err
	}
	atomic.AddUint32(&s.sendSize, ^(maxSize - 1))
	return int(maxSize), nil
WAIT:
	var timeout <-chan time.Time
	if deadline := s.deadlineWrite.Load().(time.Time); !deadline.IsZero() {
		timeout = time.After(deadline.Sub(time.Now()))
	}
	select {
	case <-s.sendNotify:
		goto BEGIN
	case <-timeout:
		return 0, ErrTimeout
	}
}
func (s *Stream) sendFlags() uint16 {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()
	var flags uint16
	switch s.state {
	case stateInit:
		flags |= flagSYN
		s.state = stateOpen
	case stateRecv:
		flags |= flagACK
		s.state = stateEstablished
	default:
	}
	return flags
}
func (s *Stream) sendUpdate() error {
	s.controlLock.Lock()
	defer s.controlLock.Unlock()
	maxSize := s.session.conf.MaxStreamSize
	var bufLen uint32
	s.recvLock.Lock()
	if s.recvBuff != nil {
		bufLen = uint32(s.recvBuff.Len())
	}
	delta := (maxSize - bufLen) - s.recvSize
	flags := s.sendFlags()
	if delta < (maxSize/2) && flags == 0 {
		s.recvLock.Unlock()
		return nil
	}
	s.recvSize += delta
	s.recvLock.Unlock()

	s.controlHead.encode(msgTypeUpdate, flags, s.id, delta)
	if err := s.session.waitSendErr(s.controlHead, nil, s.controlErr); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrTimeout) {
			s.controlHead = make([]byte, headerSize)
		}
		return err
	}
	return nil
}
func (s *Stream) sendClose() error {
	s.controlLock.Lock()
	defer s.controlLock.Unlock()
	flags := s.sendFlags()
	flags |= flagFIN
	s.controlHead.encode(msgTypeUpdate, flags, s.id, 0)
	if err := s.session.waitSendErr(s.controlHead, nil, s.controlErr); err != nil {
		if errors.Is(err, ErrSessionShutdown) || errors.Is(err, ErrTimeout) {
			s.controlHead = make([]byte, headerSize)
		}
		return err
	}
	return nil
}
func (s *Stream) Close() error {
	var closeStream bool
	s.stateLock.Lock()
	switch s.state {
	case stateOpen:
		fallthrough
	case stateRecv:
		fallthrough
	case stateEstablished:
		s.state = stateLocalClose
		goto CLOSE
	case stateLocalClose:
	case stateRemoteClose:
		s.state = stateClosed
		closeStream = true
		goto CLOSE
	default:
	}
	s.stateLock.Unlock()
	return nil
CLOSE:
	if s.closeTimer != nil {
		s.closeTimer.Stop()
		s.closeTimer = nil
	}
	if !closeStream && s.session.conf.TimeoutStreamClose > 0 {
		s.closeTimer = time.AfterFunc(
			s.session.conf.TimeoutStreamClose,
			s.closeTimeout,
		)
	}
	s.stateLock.Unlock()
	_ = s.sendClose()
	s.notifyWaiting()
	if closeStream {
		s.session.closeStream(s.id)
	}
	return nil
}
func (s *Stream) closeTimeout() {
	s.forceClose()
	s.session.closeStream(s.id)
	s.sendLock.Lock()
	defer s.sendLock.Unlock()
	hd := newHeader()
	hd.encode(msgTypeUpdate, flagRST, s.id, 0)
	_ = s.session.sendWithoutWait(hd)
}
func (s *Stream) forceClose() {
	s.stateLock.Lock()
	s.state = stateClosed
	s.stateLock.Unlock()
	s.notifyWaiting()
}
func (s *Stream) notifyWaiting() {
	sendNotify(s.recvNotify)
	sendNotify(s.sendNotify)
	sendNotify(s.establishNotify)
}
func (s *Stream) incrSendWindow(hdr header, flags uint16) error {
	if err := s.processFlags(flags); err != nil {
		return err
	}
	atomic.AddUint32(&s.sendSize, hdr.Length())
	sendNotify(s.sendNotify)
	return nil
}
func (s *Stream) processFlags(flags uint16) error {
	s.stateLock.Lock()
	defer s.stateLock.Unlock()

	closeStream := false
	defer func() {
		if closeStream {
			if s.closeTimer != nil {
				s.closeTimer.Stop()
			}
			s.session.closeStream(s.id)
		}
	}()

	if flags&flagACK == flagACK {
		if s.state == stateOpen {
			s.state = stateEstablished
		}
		sendNotify(s.establishNotify)
		s.session.establishStream(s.id)
	}
	if flags&flagFIN == flagFIN {
		switch s.state {
		case stateOpen:
			fallthrough
		case stateRecv:
			fallthrough
		case stateEstablished:
			s.state = stateRemoteClose
			s.notifyWaiting()
		case stateLocalClose:
			s.state = stateClosed
			closeStream = true
			s.notifyWaiting()
		default:
			s.session.conf.LogWriter.Errorf("unexpected FIN flag in state %d", s.state)
			return ErrUnexpectedFlag
		}
	}
	if flags&flagRST == flagRST {
		s.state = stateReset
		closeStream = true
		s.notifyWaiting()
	}
	return nil
}
func (s *Stream) readData(hdr header, flags uint16, conn io.Reader) error {
	if err := s.processFlags(flags); err != nil {
		return err
	}

	length := hdr.Length()
	if length == 0 {
		return nil
	}

	conn = &io.LimitedReader{R: conn, N: int64(length)}

	s.recvLock.Lock()
	if length > s.recvSize {
		s.session.conf.LogWriter.Errorf("receive window exceeded (stream: %d, remain: %d, recv: %d)", s.id, s.recvSize, length)
		s.recvLock.Unlock()
		return ErrRecvSizeExceeded
	}

	if s.recvBuff == nil {
		s.recvBuff = bytes.NewBuffer(make([]byte, 0, length))
	}
	copiedLength, err := io.Copy(s.recvBuff, conn)
	if err != nil {
		s.session.conf.LogWriter.Errorf("failed to read stream data: %v", err)
		s.recvLock.Unlock()
		return err
	}

	s.recvSize -= uint32(copiedLength)
	s.recvLock.Unlock()

	sendNotify(s.recvNotify)
	return nil
}
func (s *Stream) Shrink() {
	s.recvLock.Lock()
	if s.recvBuff != nil && s.recvBuff.Len() == 0 {
		s.recvBuff = nil
	}
	s.recvLock.Unlock()
}
