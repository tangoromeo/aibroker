package transport

import (
	"errors"
	"io"

	"aibroker/internal/jsonrpc"
)

type StdioTransport struct {
	reader io.Reader
	writer io.Writer
	codec  *jsonrpc.Codec
}

func NewStdioTransport(r io.Reader, w io.Writer) *StdioTransport {
	return &StdioTransport{
		reader: r,
		writer: w,
		codec:  jsonrpc.NewCodec(r, w),
	}
}

func (t *StdioTransport) Read() (*jsonrpc.Message, error) {
	return t.codec.Read()
}

func (t *StdioTransport) Write(msg *jsonrpc.Message) error {
	return t.codec.Write(msg)
}

// Close attempts to close the underlying reader/writer to unblock pending reads.
func (t *StdioTransport) Close() error {
	var errs []error
	if c, ok := t.reader.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	if c, ok := t.writer.(io.Closer); ok {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}
