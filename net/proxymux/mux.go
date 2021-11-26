// Copyright (c) 2021 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package proxymux splits a net.Listener in two, routing SOCKS5
// connections to one and HTTP requests to the other.
//
// It allows for hosting both a SOCKS5 proxy and an HTTP proxy on the
// same listener.
package proxymux

import (
	"io"
	"net"
	"sync"
)

// Mux accepts connections on l and passes connections through to
// either socksListener or httpListener, depending the first byte sent
// by the client.
func Mux(l net.Listener) (socksListener, httpListener net.Listener) {
	sl := &listener{
		addr:   l.Addr(),
		c:      make(chan net.Conn),
		closed: make(chan struct{}),
	}
	hl := &listener{
		addr:   l.Addr(),
		c:      make(chan net.Conn),
		closed: make(chan struct{}),
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				socksListener.Close()
				httpListener.Close()
				return
			}
			go routeConn(conn, sl, hl)
		}
	}()

	return sl, hl
}

func routeConn(c net.Conn, socksListener, httpListener *listener) {
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		c.Close()
		return
	}
	conn := &connWithOneByte{
		Conn: c,
		b:    b[0],
	}

	// First byte of a SOCKS5 session is a version byte set to 5.
	var l *listener
	if b[0] == 5 {
		l = socksListener
	} else {
		l = httpListener
	}
	select {
	case l.c <- conn:
	case <-l.closed:
		c.Close()
	}
}

type listener struct {
	addr   net.Addr
	c      chan net.Conn
	mu     sync.Mutex // serializes close() on closed. It's okay to receive on closed without locking.
	closed chan struct{}
}

func (l *listener) Accept() (net.Conn, error) {
	// Once closed, reliably stay closed, don't race with attempts at
	// further connections.
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	select {
	case ret := <-l.c:
		return ret, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		// Already closed
	default:
		close(l.closed)
	}
	return nil
}

func (l *listener) Addr() net.Addr {
	return l.addr
}

// connWithOneByte is a net.Conn that returns b for the first read
// request, then forwards everything else to Conn.
type connWithOneByte struct {
	net.Conn

	mu    sync.Mutex
	b     byte
	bRead bool
}

func (c *connWithOneByte) Read(bs []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bRead {
		return c.Conn.Read(bs)
	}
	if len(bs) == 0 {
		return 0, nil
	}
	c.bRead = true
	bs[0] = c.b
	return 1, nil
}
