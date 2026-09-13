package routeros

import (
	"errors"
	"fmt"

	"github.com/go-routeros/routeros/v3/proto"
)

var (
	errAlreadyAsync   = errors.New("method Async() has already been called")
	errAsyncLoopEnded = errors.New("method Async(): loop has ended - probably read error")
)

// UnknownReplyError records the sentence whose Word is unknown.
type UnknownReplyError struct {
	Sentence *proto.Sentence
}

func (err *UnknownReplyError) Error() string {
	return "unknown RouterOS reply word: " + err.Sentence.Word
}

// DeviceError records the sentence containing the error received from the device.
// The sentence may have Word !trap or !fatal.
type DeviceError struct {
	Sentence *proto.Sentence
}

func (err *DeviceError) fetchMessage() string {
	if m := err.Sentence.Map["message"]; m != "" {
		return m
	}

	return "unknown error: " + err.Sentence.String()
}

func (err *DeviceError) Error() string {
	return fmt.Sprintf("from RouterOS device: %s", err.fetchMessage())
}
