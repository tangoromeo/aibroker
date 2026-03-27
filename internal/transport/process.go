package transport

import (
	"context"
	"io"
	"os"
	"os/exec"

	"aibroker/internal/jsonrpc"
)

type ProcessTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	codec *jsonrpc.Codec
}

func NewProcessTransport(ctx context.Context, command string, args []string, env []string) (*ProcessTransport, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stderr = os.Stderr
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &ProcessTransport{
		cmd:   cmd,
		stdin: stdin,
		codec: jsonrpc.NewCodec(stdout, stdin),
	}, nil
}

func (t *ProcessTransport) Read() (*jsonrpc.Message, error) {
	return t.codec.Read()
}

func (t *ProcessTransport) Write(msg *jsonrpc.Message) error {
	return t.codec.Write(msg)
}

// CloseWrite closes the agent's stdin pipe, signaling EOF to the subprocess.
// The agent can still produce output on stdout after this call.
func (t *ProcessTransport) CloseWrite() error {
	return t.stdin.Close()
}

func (t *ProcessTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	_ = t.cmd.Wait()
	return nil
}
