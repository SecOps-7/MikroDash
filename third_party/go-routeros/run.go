package routeros

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-routeros/routeros/v3/proto"
)

type asyncReply struct {
	chanReply
	Reply
}

// Run simply calls RunArgs().
func (c *Client) Run(sentences ...string) (*Reply, error) {
	return c.RunArgs(sentences)
}

// RunContext simply calls RunArgsContext().
func (c *Client) RunContext(ctx context.Context, sentences ...string) (*Reply, error) {
	return c.RunArgsContext(ctx, sentences)
}

// RunArgs sends a sentence to the RouterOS device and waits for the reply.
func (c *Client) RunArgs(sentences []string) (*Reply, error) {
	return c.RunArgsContext(context.Background(), sentences)
}

// RunArgsContext sends a sentence to the RouterOS device and waits for the reply.
func (c *Client) RunArgsContext(ctx context.Context, sentences []string) (*Reply, error) {
	c.logger().Debug("RunArgsContext", slog.Any("sentences", sentences))

	c.w.BeginSentence()
	for _, sentence := range sentences {
		c.w.WriteWord(sentence)
	}

	if !c.IsAsync() {
		return c.runArgsContextSync()
	}

	// async mode, assign new tag to request
	tag := c.incrementTag()

	a := &asyncReply{}
	a.reC = make(chan *proto.Sentence)
	a.tag = fmt.Sprintf("r%d", tag)
	c.w.WriteWord(".tag=" + a.tag)
	c.logger().Debug("set tag", slog.String("tag", a.tag))

	// MIKRODASH PATCH (see PATCHES.md): register the tag BEFORE the sentence is
	// sent. Upstream registered it after EndSentence, and asyncLoop discards a
	// sentence whose tag is not in the map, so a reply that arrived in between
	// was lost and this call waited for ever. No write happens under c.mu.
	c.mu.Lock()
	registered := c.tags != nil
	if registered {
		c.tags[a.tag] = a
	}
	c.mu.Unlock()

	if err := c.w.EndSentence(); err != nil {
		c.mu.Lock()
		delete(c.tags, a.tag)
		c.mu.Unlock()

		return nil, err
	}

	if !registered {
		return nil, errAsyncLoopEnded
	}

	// wait for asyncLoop to close channel or context done
	for {
		select {
		case <-ctx.Done():
			c.r.Cancel()

			return nil, ctx.Err()
		case _, ok := <-a.reC:
			if !ok { // channel closed
				return &a.Reply, a.err
			}
		}
	}
}

// runArgsContextSync - read command reply in sync mode and return
func (c *Client) runArgsContextSync() (*Reply, error) {
	var err error
	if err = c.w.EndSentence(); err != nil {
		return nil, err
	}

	out := new(Reply)

	var lastErr error
	for {
		var sen *proto.Sentence

		// read next sentence
		if sen, err = c.r.ReadSentence(); err != nil {
			return nil, err
		}

		var done bool

		switch done, err = out.processSentence(sen); {
		case err != nil && done:
			// processed error sentence and it was fatal
			return nil, err
		case err != nil:
			// processed error sentence, but it was not fatal, read next, store last error
			lastErr = err
		case done:
			// processed sentence is Done, return result and last error
			return out, lastErr
		}
	}
}
