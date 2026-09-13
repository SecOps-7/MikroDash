package proto

import (
	"bufio"
	"io"
	"sync/atomic"
)

type ctxResult struct {
	num int
	err error
}

type ctxReader struct {
	io.Reader
	close atomic.Bool
	done  chan struct{}
}

func (c *ctxReader) Close() {
	if c.close.Load() {
		return
	}
	c.close.Store(true)
	close(c.done)
}

func (c *ctxReader) Cancel() {
	if c.close.Load() {
		return
	}

	select {
	case c.done <- struct{}{}:
	default:
	}
}

func (c *ctxReader) Read(p []byte) (int, error) {
	out := make(chan *ctxResult, 1)
	buf := make([]byte, len(p))

	go func() {
		res := new(ctxResult)
		res.num, res.err = c.Reader.Read(buf)
		out <- res
		close(out)
	}()

	select {
	case <-c.done:
		return 0, io.EOF
	case r := <-out:
		copy(p, buf)

		return r.num, r.err
	}
}

type ctxWriter struct {
	*bufio.Writer
	close atomic.Bool
	done  chan struct{}
}

func (c *ctxWriter) Close() {
	if c.close.Load() {
		return
	}
	c.close.Store(true)
	close(c.done)
}

func (c *ctxWriter) Cancel() {
	if c.close.Load() {
		return
	}

	select {
	case c.done <- struct{}{}:
	default:
	}
}

func (c *ctxWriter) Write(p []byte) (int, error) {
	out := make(chan *ctxResult, 1)
	buf := make([]byte, len(p))
	copy(buf, p)

	go func() {
		res := new(ctxResult)
		res.num, res.err = c.Writer.Write(buf)
		out <- res
		close(out)
	}()

	select {
	case <-c.done:
		return 0, io.EOF
	case r := <-out:
		return r.num, r.err
	}
}
