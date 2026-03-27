package jsonrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Codec reads and writes newline-delimited JSON-RPC messages.
// Both Read and Write are safe for concurrent use.
type Codec struct {
	scanner *bufio.Scanner
	writer  io.Writer
	mu      sync.Mutex // protects writer
	rmu     sync.Mutex // protects scanner
}

func NewCodec(r io.Reader, w io.Writer) *Codec {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // up to 10MB per message
	return &Codec{
		scanner: s,
		writer:  w,
	}
}

func (c *Codec) Read() (*Message, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("json decode: %w", err)
		}
		return &msg, nil
	}
	if err := c.scanner.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return nil, io.EOF
}

func (c *Codec) Write(msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("json encode: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	_, err = c.writer.Write(data)
	return err
}
