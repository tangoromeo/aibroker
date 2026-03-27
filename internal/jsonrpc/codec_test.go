package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)

	req := &Message{
		JSONRPC: Version,
		ID:      json.RawMessage(`1`),
		Method:  "add",
		Params:  json.RawMessage(`[1,2]`),
	}
	resp := &Message{
		JSONRPC: Version,
		ID:      json.RawMessage(`1`),
		Result:  json.RawMessage(`3`),
	}
	notif := &Message{
		JSONRPC: Version,
		Method:  "ping",
		Params:  json.RawMessage(`{}`),
	}

	for _, want := range []*Message{req, resp, notif} {
		if err := c.Write(want); err != nil {
			t.Fatal(err)
		}
		got, err := c.Read()
		if err != nil {
			t.Fatal(err)
		}
		assertMessagesJSONEqual(t, want, got)
	}
}

func TestCodecEmptyLines(t *testing.T) {
	raw := strings.Join([]string{
		"",
		`{"jsonrpc":"2.0","id":1,"method":"m"}`,
		"",
		"",
		`{"jsonrpc":"2.0","method":"n"}`,
		"",
	}, "\n")
	r := strings.NewReader(raw)
	var w bytes.Buffer
	c := NewCodec(r, &w)

	m1, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if string(m1.ID) != "1" || m1.Method != "m" {
		t.Fatalf("first message: %+v", m1)
	}
	m2, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.ID) != 0 || m2.Method != "n" {
		t.Fatalf("second message: %+v", m2)
	}
}

func TestCodecLargeMessage(t *testing.T) {
	large := string(bytes.Repeat([]byte("x"), 2*1024*1024))
	params, err := json.Marshal(struct {
		Blob string `json:"blob"`
	}{Blob: large})
	if err != nil {
		t.Fatal(err)
	}
	want := &Message{
		JSONRPC: Version,
		ID:      json.RawMessage(`99`),
		Method:  "bulk",
		Params:  params,
	}

	var buf bytes.Buffer
	c := NewCodec(&buf, &buf)
	if err := c.Write(want); err != nil {
		t.Fatal(err)
	}
	got, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	assertMessagesJSONEqual(t, want, got)
}

func TestCodecConcurrentWrites(t *testing.T) {
	pr, pw := io.Pipe()
	cRead := NewCodec(pr, io.Discard)
	cWrite := NewCodec(strings.NewReader(""), pw)

	const n = 50
	readDone := make(chan error, 1)
	go func() {
		seen := make(map[int]struct{})
		for range n {
			m, err := cRead.Read()
			if err != nil {
				readDone <- err
				return
			}
			var id int
			if err := json.Unmarshal(m.ID, &id); err != nil {
				readDone <- err
				return
			}
			if _, dup := seen[id]; dup {
				readDone <- fmt.Errorf("duplicate id %d", id)
				return
			}
			seen[id] = struct{}{}
		}
		if _, err := cRead.Read(); err != io.EOF {
			if err != nil {
				readDone <- err
			} else {
				readDone <- fmt.Errorf("expected EOF")
			}
			return
		}
		readDone <- nil
	}()

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			idb, _ := json.Marshal(idx)
			msg := &Message{JSONRPC: Version, ID: idb, Method: "m"}
			errs[idx] = cWrite.Write(msg)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
}

func assertMessagesJSONEqual(t *testing.T, a, b *Message) {
	t.Helper()
	aj, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(aj) != string(bj) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", bj, aj)
	}
}
