package transport

import "aibroker/internal/jsonrpc"

// Transport abstracts bidirectional JSON-RPC message exchange
// over any wire protocol (stdio, HTTP/SSE, etc.).
type Transport interface {
	Read() (*jsonrpc.Message, error)
	Write(msg *jsonrpc.Message) error
	Close() error
}
