package jsonrpc

import "encoding/json"

const Version = "2.0"

// Message represents a JSON-RPC 2.0 message: request, response, or notification.
// Fields are kept as json.RawMessage to allow transparent proxying without
// interpreting protocol-specific payloads.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (m *Message) IsRequest() bool {
	return len(m.ID) > 0 && m.Method != ""
}

func (m *Message) IsResponse() bool {
	return len(m.ID) > 0 && m.Method == ""
}

func (m *Message) IsNotification() bool {
	return len(m.ID) == 0 && m.Method != ""
}

func NewError(id json.RawMessage, code int, message string) *Message {
	return &Message{
		JSONRPC: Version,
		ID:      id,
		Error:   &Error{Code: code, Message: message},
	}
}

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)
