package routeros

import (
	"io"
	"testing"
	"unsafe"
)

func TestNextTagAlignedForAtomic(t *testing.T) {
	c, err := NewClient(nopRWC{})
	if err != nil {
		t.Fatal(err)
	}
	off := unsafe.Offsetof(c.nextTag)
	if off%8 != 0 {
		t.Fatalf("nextTag offset %d is not 8-byte aligned", off)
	}
	// Smoke: concurrent increments must not panic on any GOARCH.
	const n = 1000
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			_ = c.incrementTag()
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if got := c.nextTag.Load(); got != n {
		t.Fatalf("nextTag = %d, want %d", got, n)
	}
}

type nopRWC struct{}

func (nopRWC) Read(p []byte) (int, error)  { return 0, io.EOF }
func (nopRWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopRWC) Close() error                { return nil }
