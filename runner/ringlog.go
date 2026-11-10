package main

import (
	"bytes"
	"fmt"
	"sync"
	"time"
)

// ringLog is an io.Writer that keeps the last n lines of command output.
type ringLog struct {
	mu      sync.Mutex
	max     int
	buf     []string
	partial []byte
}

func newRingLog(max int) *ringLog { return &ringLog{max: max} }

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := append(r.partial, p...)
	for {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			break
		}
		if i > 0 {
			r.push(string(data[:i]))
		}
		data = data[i+1:]
	}
	if len(data) > 8192 { // very long line without newline
		r.push(string(data))
		data = nil
	}
	r.partial = append([]byte(nil), data...)
	return len(p), nil
}

func (r *ringLog) push(line string) {
	r.buf = append(r.buf, line)
	if len(r.buf) > r.max {
		r.buf = append([]string(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

func (r *ringLog) Printf(format string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.push(time.Now().UTC().Format("15:04:05 ") + fmt.Sprintf(format, a...))
}

func (r *ringLog) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.buf))
	copy(out, r.buf)
	return out
}
