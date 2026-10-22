package fence

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// The largest frame either end sends: an IPv4 or IPv6 packet of 64 KiB,
// which is what the guest's network offers it, and a little over.
const maxFrame = 1 << 17

// Relay carries frames between a guest's network card and its network
// until either hangs up, and holds every frame from the guest to the policy
// in force when it is sent. Each frame on either stream is preceded by its
// length, four bytes in network order: QEMU's stream network device and
// passt both speak that.
func Relay(guest, network io.ReadWriteCloser, policy func() *Policy) error {
	var toGuest sync.Mutex
	answer := func(frame []byte, scratch *[]byte) error {
		toGuest.Lock()
		defer toGuest.Unlock()
		return writeFrame(guest, frame, scratch)
	}
	done := make(chan error, 2)
	go func() {
		reader := bufio.NewReaderSize(network, maxFrame)
		buffer, scratch := make([]byte, maxFrame), make([]byte, 0, maxFrame+4)
		for {
			frame, err := readFrame(reader, buffer)
			if err != nil {
				done <- err
				return
			}
			if err := answer(frame, &scratch); err != nil {
				done <- err
				return
			}
		}
	}()
	go func() {
		reader := bufio.NewReaderSize(guest, maxFrame)
		buffer, scratch := make([]byte, maxFrame), make([]byte, 0, maxFrame+4)
		var answers []byte
		replies := bucket{tokens: replyBurst}
		for {
			frame, err := readFrame(reader, buffer)
			if err != nil {
				done <- err
				return
			}
			pass, reply := policy().Check(frame)
			switch {
			case pass:
				err = writeFrame(network, frame, &scratch)
			case reply != nil && replies.take(time.Now()):
				err = answer(reply, &answers)
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err := <-done
	guest.Close()
	network.Close()
	<-done
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// readFrame reads one frame into buffer, and returns it.
func readFrame(reader *bufio.Reader, buffer []byte) ([]byte, error) {
	var length [4]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(length[:])
	if size > uint32(len(buffer)) {
		return nil, fmt.Errorf("a frame of %d bytes is more than any network card sends", size)
	}
	frame := buffer[:size]
	if _, err := io.ReadFull(reader, frame); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return frame, nil
}

// writeFrame writes one frame and its length in a single write, so a
// frame is never split by another written beside it.
func writeFrame(writer io.Writer, frame []byte, scratch *[]byte) error {
	out := binary.BigEndian.AppendUint32((*scratch)[:0], uint32(len(frame)))
	out = append(out, frame...)
	*scratch = out
	_, err := writer.Write(out)
	return err
}

// How many refusals a guest is told of: replyRate a second, after a first
// replyBurst. Each is built and written by hived, whose time every job
// shares, so a guest sending refused frames as fast as it can is told of
// some and not all: a connect() fails fast all the same, and the rest
// time out.
const (
	replyRate  = 100
	replyBurst = 100
)

// bucket is a token bucket of replies.
type bucket struct {
	tokens float64
	last   time.Time
}

// take spends a token if there is one.
func (b *bucket) take(now time.Time) bool {
	if !b.last.IsZero() {
		b.tokens = min(replyBurst, b.tokens+now.Sub(b.last).Seconds()*replyRate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
