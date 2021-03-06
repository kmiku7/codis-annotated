// Copyright 2014 Wandoujia Inc. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package redis

import (
	"net"
	"time"

	"github.com/wandoulabs/codis/pkg/utils/errors"
)

// 封装关系
// 循环引用了
// Conn <- Decoder <- connReader <- Conn
type Conn struct {
	Sock net.Conn

	// 这两个超时时间的设置见 router/session.go, NewSessionSize()
	ReaderTimeout time.Duration
	WriterTimeout time.Duration

	// 这两个是指针, 何时赋值初始化, 对Sock的包装,
	// 默认 bufsize 为64k.
	Reader *Decoder
	Writer *Encoder
}

func DialTimeout(addr string, bufsize int, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return NewConnSize(c, bufsize), nil
}

func NewConn(sock net.Conn) *Conn {
	return NewConnSize(sock, 1024*64)
}

func NewConnSize(sock net.Conn, bufsize int) *Conn {
	conn := &Conn{Sock: sock}
	conn.Reader = NewDecoderSize(&connReader{Conn: conn}, bufsize)
	conn.Writer = NewEncoderSize(&connWriter{Conn: conn}, bufsize)
	return conn
}

func (c *Conn) Close() error {
	return c.Sock.Close()
}

type connReader struct {
	*Conn
	hasDeadline bool
}

func (r *connReader) Read(b []byte) (int, error) {
	// 这两个 if-else 什么鬼?
	// 如果有超时时间则设置, 如果没有且hasDeadline == true, 则增加一个取消超时的操作
	if timeout := r.ReaderTimeout; timeout != 0 {
		if err := r.Sock.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return 0, errors.Trace(err)
		}
		r.hasDeadline = true
	} else if r.hasDeadline {
		if err := r.Sock.SetReadDeadline(time.Time{}); err != nil {
			return 0, errors.Trace(err)
		}
		r.hasDeadline = false
	}
	n, err := r.Sock.Read(b)
	if err != nil {
		err = errors.Trace(err)
	}
	return n, err
}

type connWriter struct {
	*Conn
	hasDeadline bool
}

// 这里的 write/read 什么场景会用?
func (w *connWriter) Write(b []byte) (int, error) {
	if timeout := w.WriterTimeout; timeout != 0 {
		if err := w.Sock.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return 0, errors.Trace(err)
		}
		w.hasDeadline = true
	} else if w.hasDeadline {
		if err := w.Sock.SetWriteDeadline(time.Time{}); err != nil {
			return 0, errors.Trace(err)
		}
		w.hasDeadline = false
	}
	n, err := w.Sock.Write(b)
	if err != nil {
		err = errors.Trace(err)
	}
	return n, err
}

func IsTimeout(err error) bool {
	if err := errors.Cause(err); err != nil {
		e, ok := err.(*net.OpError)
		if ok {
			return e.Timeout()
		}
	}
	return false
}
