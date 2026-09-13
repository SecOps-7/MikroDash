package routeros

import (
	"context"

	"github.com/go-routeros/routeros/v3/proto"
)

type sentenceProcessor interface {
	processSentence(sen *proto.Sentence) (bool, error)
}

type replyCloser interface {
	close(err error)
}

// Async starts asynchronous mode and returns immediately.
func (c *Client) Async() <-chan error {
	return c.AsyncContext(context.Background())
}

// AsyncContext starts asynchronous mode with context and returns immediately.
func (c *Client) AsyncContext(ctx context.Context) <-chan error {
	c.mu.Lock()
	defer c.mu.Unlock()

	errC := make(chan error, 1)
	if c.async {
		errC <- errAlreadyAsync
		close(errC)
		return errC
	}
	c.async = true
	c.tags = make(map[string]sentenceProcessor)
	go c.asyncLoopChan(ctx, errC)
	return errC
}

func (c *Client) asyncLoopChan(ctx context.Context, errC chan<- error) {
	defer close(errC)

	// If c.Close() has been called, c.closing will be true, and
	// err will be “use of closed network connection”. Ignore that error.
	if err := c.asyncLoop(ctx); err != nil {
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if !closing {
			errC <- err
		}
	}
}

// asyncLoop - main goroutine for async mode. Read and process sentences, handle context done.
func (c *Client) asyncLoop(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		c.r.Cancel()
	}()

	for {
		sen, err := c.r.ReadSentence()

		if err != nil {
			c.closeTags(err)
			return err
		}

		c.mu.Lock()
		r, ok := c.tags[sen.Tag]
		c.mu.Unlock()

		// cannot find tag for this sentence, ignore
		if !ok {
			continue
		}

		done, err := r.processSentence(sen)
		if done || err != nil {
			c.mu.Lock()
			delete(c.tags, sen.Tag)
			c.mu.Unlock()
			closeReply(r, err)
		}
	}
}

func (c *Client) closeTags(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If c.Close() has been called, c.closing will be true, and
	// err will be “use of closed network connection”. Ignore that error.
	if c.closing {
		for _, r := range c.tags {
			closeReply(r, nil)
		}

		c.tags = nil

		return
	}

	for _, r := range c.tags {
		closeReply(r, err)
	}

	c.tags = nil
}

func closeReply(r sentenceProcessor, err error) {
	rr, ok := r.(replyCloser)
	if ok {
		rr.close(err)
	}
}
