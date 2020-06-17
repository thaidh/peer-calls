package udpmux

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/peer-calls/peer-calls/server/logger"
)

var DefaultMTU uint32 = 8192

type UDPMux struct {
	params    *Params
	conns     map[net.Addr]*muxedConn
	mu        sync.RWMutex
	logger    logger.Logger
	connChan  chan Conn
	closeOnce sync.Once
}

type Params struct {
	Conn          net.PacketConn
	MTU           uint32
	LoggerFactory logger.LoggerFactory
	ReadChanSize  int
}

func New(params Params) *UDPMux {
	mux := &UDPMux{
		params:   &params,
		conns:    map[net.Addr]*muxedConn{},
		logger:   params.LoggerFactory.GetLogger("udpmux"),
		connChan: make(chan Conn),
	}

	if mux.params.MTU == 0 {
		mux.params.MTU = DefaultMTU
	}

	go mux.start()

	return mux
}

func (u *UDPMux) AcceptConn() (Conn, error) {
	conn, ok := <-u.connChan
	if !ok {
		return nil, fmt.Errorf("Conn closed")
	}
	return conn, nil
}

func (u *UDPMux) GetConn(raddr net.Addr) (Conn, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// TODO return err when not connected
	return u.getOrCreateConn(raddr, false), nil
}

func (u *UDPMux) start() {
	buf := make([]byte, u.params.MTU)

	for {
		i, raddr, err := u.params.Conn.ReadFrom(buf)

		if err != nil {
			u.logger.Println("Error reading remote data: %s", err)
			_ = u.params.Conn.Close()
			return
		}

		u.mu.Lock()
		u.handleRemoteBytes(raddr, buf[:i])
		u.mu.Unlock()
	}
}

func (u *UDPMux) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.close()

	return nil
}

func (u *UDPMux) close() {
	for _, conn := range u.conns {
		conn.onceClose.Do(func() {
			close(conn.readChan)
		})
		delete(u.conns, conn.RemoteAddr())
	}

	u.closeOnce.Do(func() {
		close(u.connChan)
		_ = u.params.Conn.Close()
	})
}

func (u *UDPMux) handleClose(conn *muxedConn) {
	u.mu.Lock()
	defer u.mu.Unlock()

	conn.onceClose.Do(func() {
		close(conn.readChan)
		close(conn.closeChan)
	})
	delete(u.conns, conn.RemoteAddr())
}

func (u *UDPMux) handleRemoteBytes(raddr net.Addr, buf []byte) Conn {
	c := u.getOrCreateConn(raddr, true)
	c.handleRemoteBytes(buf)

	return c
}

func (u *UDPMux) getOrCreateConn(raddr net.Addr, accept bool) *muxedConn {
	c, ok := u.conns[raddr]
	if !ok {
		c = u.createConn(raddr, accept)
	}
	return c
}

func (u *UDPMux) createConn(raddr net.Addr, accept bool) *muxedConn {
	c := &muxedConn{
		conn:      u.params.Conn,
		laddr:     u.params.Conn.LocalAddr(),
		raddr:     raddr,
		readChan:  make(chan []byte, u.params.ReadChanSize),
		closeChan: make(chan struct{}),
		onClose:   u.handleClose,
	}
	u.conns[raddr] = c
	if accept {
		u.connChan <- c
	}
	return c
}

type Conn interface {
	net.Conn
	CloseChannel() <-chan struct{}
}

type muxedConn struct {
	conn      net.PacketConn
	laddr     net.Addr
	raddr     net.Addr
	readChan  chan []byte
	closeChan chan struct{}
	onClose   func(m *muxedConn)
	onceClose sync.Once
}

var _ Conn = &muxedConn{}

func (m *muxedConn) Close() error {
	m.onClose(m)
	return nil
}

func (m *muxedConn) handleRemoteBytes(buf []byte) {
	b := make([]byte, len(buf))
	copy(b, buf)
	m.readChan <- b
}

func (m *muxedConn) CloseChannel() <-chan struct{} {
	return m.closeChan
}

func (m *muxedConn) Read(b []byte) (int, error) {
	buf, ok := <-m.readChan
	if !ok {
		return 0, fmt.Errorf("Conn closed")
	}
	copy(b, buf)
	return len(buf), nil
}

func (m *muxedConn) Write(b []byte) (int, error) {
	select {
	case <-m.closeChan:
		return 0, fmt.Errorf("Conn is closed")
	default:
		return m.conn.WriteTo(b, m.raddr)
	}
}

func (m *muxedConn) LocalAddr() net.Addr {
	return m.laddr
}

func (m *muxedConn) RemoteAddr() net.Addr {
	return m.raddr
}

func (m *muxedConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *muxedConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *muxedConn) SetWriteDeadline(t time.Time) error {
	return nil
}

var _ net.Conn = &muxedConn{}