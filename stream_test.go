package vaif

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// errReset stands for a connection the server dropped mid-stream.
var errReset = errors.New("connection reset by peer")

// failingBody yields data and then fails, as a dropped event stream does.
type failingBody struct {
	data io.Reader
	end  func() error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.data.Read(p)
	if err == io.EOF {
		return n, b.end()
	}
	return n, err
}
func (b *failingBody) Close() error { return nil }

func streamClient(t *testing.T, body io.ReadCloser) *Client {
	t.Helper()
	return parityClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
	})
}

// A dropped event stream is a *Error matching ErrNetwork; the transport's own
// error stays reachable through errors.Is.
func TestEventStreamReadFailureIsAnSDKError(t *testing.T) {
	c := streamClient(t, &failingBody{data: strings.NewReader("data: {}\n\n"), end: func() error { return errReset }})
	resp, err := c.Operations.PostAiCopilotChatStream(context.Background(), OperationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrNetwork) || errors.Is(err, ErrTimeout) || !errors.Is(err, errReset) {
		t.Fatalf("read error = %#v; want a *Error matching ErrNetwork that wraps the reset", err)
	}
	if string(data) != "data: {}\n\n" {
		t.Fatalf("data = %q; want the event read before the failure", data)
	}
}

// A canceled context ends the stream with a *Error that still matches
// context.Canceled.
func TestEventStreamCanceledContextIsAnSDKError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := streamClient(t, &failingBody{data: strings.NewReader(""), end: func() error { cancel(); return context.Canceled }})
	resp, err := c.Operations.PostAiCopilotChatStream(ctx, OperationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || !errors.Is(err, ErrNetwork) || !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %#v; want a *Error matching ErrNetwork that wraps context.Canceled", err)
	}
}

// Guard: the end of the stream is io.EOF, as io.Reader requires.
func TestEventStreamEndIsEOF(t *testing.T) {
	c := streamClient(t, io.NopCloser(strings.NewReader("data: {}\n\n")))
	resp, err := c.Operations.PostAiCopilotChatStream(context.Background(), OperationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, err := io.ReadFull(resp.Body, buf)
	if err != io.ErrUnexpectedEOF || string(buf[:n]) != "data: {}\n\n" {
		t.Fatalf("ReadFull = %q, %v; want the event, then the stream's end", buf[:n], err)
	}
	if n, err := resp.Body.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("Read after the end = %d, %v; want 0, io.EOF", n, err)
	}
}
