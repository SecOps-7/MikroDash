package routeros

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-routeros/routeros/v3/proto"
)

const (
	fatalSentence = "!fatal"
	doneSentence  = "!done"
	trapSentence  = "!trap"
	reSentence    = "!re"
	emptySentence = "!empty"
)

// ListenReply is the struct returned by the Listen*() functions.
// When the channel returned by Chan() is closed, Done is set to the
// RouterOS sentence that caused it to be closed.
type ListenReply struct {
	chanReply
	Done *proto.Sentence
	c    *Client
}

// Chan returns a channel for receiving !re RouterOS sentences.
// To close the channel, call Cancel() on l.
func (l *ListenReply) Chan() <-chan *proto.Sentence {
	return l.reC
}

// Cancel sends a cancel command to the RouterOS device.
func (l *ListenReply) Cancel() (*Reply, error) {
	return l.c.Run("/cancel", "=tag="+l.tag)
}

// CancelContext sends a cancel command to the RouterOS device with context.
func (l *ListenReply) CancelContext(ctx context.Context) (*Reply, error) {
	return l.c.RunContext(ctx, "/cancel", "=tag="+l.tag)
}

// Listen simply calls ListenArgsQueue() with queueSize set to c.Queue.
func (c *Client) Listen(sentence ...string) (*ListenReply, error) {
	return c.ListenArgsQueue(sentence, c.Queue)
}

// ListenContext simply calls ListenArgsQueue() with queueSize set to c.Queue.
func (c *Client) ListenContext(ctx context.Context, sentence ...string) (*ListenReply, error) {
	return c.ListenArgsQueueContext(ctx, sentence, c.Queue)
}

// ListenArgs simply calls ListenArgsQueue() with queueSize set to c.Queue.
func (c *Client) ListenArgs(sentence []string) (*ListenReply, error) {
	return c.ListenArgsQueue(sentence, c.Queue)
}

// ListenArgsContext simply calls ListenArgsQueue() with queueSize set to c.Queue.
func (c *Client) ListenArgsContext(ctx context.Context, sentence []string) (*ListenReply, error) {
	return c.ListenArgsQueueContext(ctx, sentence, c.Queue)
}

// ListenArgsQueue sends a sentence to the RouterOS device and returns immediately.
func (c *Client) ListenArgsQueue(sentence []string, queueSize int) (*ListenReply, error) {
	return c.ListenArgsQueueContext(context.Background(), sentence, queueSize)
}

// ListenArgsQueueContext sends a sentence to the RouterOS device and returns immediately.
func (c *Client) ListenArgsQueueContext(ctx context.Context, sentence []string, queueSize int) (*ListenReply, error) {
	c.logger().Debug("ListenArgsQueueContext", slog.Any("sentences", sentence))

	if !c.IsAsync() {
		c.AsyncContext(ctx)
	}

	tag := c.incrementTag()

	l := &ListenReply{c: c}
	l.tag = fmt.Sprintf("l%d", tag)
	l.reC = make(chan *proto.Sentence, queueSize)

	c.w.BeginSentence()

	c.logger().Debug("set listener tag", slog.String("tag", l.tag))

	for _, word := range sentence {
		c.w.WriteWord(word)
	}
	c.w.WriteWord(".tag=" + l.tag)

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.w.EndSentence(); err != nil {
		return nil, err
	}

	if c.tags == nil {
		return nil, errAsyncLoopEnded
	}

	c.tags[l.tag] = l

	go func() {
		<-ctx.Done()

		c.r.Cancel()
	}()

	return l, nil
}

func (l *ListenReply) processSentence(sen *proto.Sentence) (bool, error) {
	switch sen.Word {
	case reSentence:
		l.reC <- sen
	case doneSentence:
		l.Done = sen
		return true, nil
	case trapSentence:
		if sen.Map["category"] == "2" {
			l.Done = sen // "execution of command interrupted"
			return true, nil
		}
		return true, &DeviceError{sen}
	case fatalSentence:
		return true, &DeviceError{sen}
	case "", emptySentence:
		// API docs say that empty sentences should be ignored
	default:
		return true, &UnknownReplyError{sen}
	}
	return false, nil
}
