// Command conformance is the adapter the VAIF Studio SDK conformance runner
// uses to drive this SDK against a local API. It reads one JSON request per
// line on standard input and writes one JSON reply per line on standard
// output. Tokens stay on these pipes and are never logged.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

type input struct {
	Op, Actor, ProjectID, APIKey, Token, Table, Bucket, BucketID string
	Args                                                         map[string]any
}
type reply struct {
	Result any    `json:"result,omitempty"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

func localURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("local HTTP only")
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	return fmt.Errorf("localhost only")
}

type localTransport struct{ base http.RoundTripper }

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := localURL(r.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r)
}
func main() {
	api := os.Getenv("API_URL")
	u, err := url.Parse(api)
	if err != nil || localURL(u) != nil {
		fmt.Fprintln(os.Stderr, "API_URL must be local HTTP")
		os.Exit(1)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	httpClient := &http.Client{Transport: localTransport{transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	clients := map[string]*vaif.Client{}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	out := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var in input
		if json.Unmarshal(scanner.Bytes(), &in) != nil {
			out.Encode(reply{Error: true, Code: "INVALID_INPUT"})
			continue
		}
		result, err := operate(context.Background(), api, httpClient, clients, in)
		if err != nil {
			r := reply{Error: true, Code: "ADAPTER_ERROR"}
			var e *vaif.Error
			if errors.As(err, &e) {
				r.Status, r.Code = e.Status, e.Code
			}
			out.Encode(r)
		} else {
			out.Encode(reply{Result: result})
		}
	}
}
func operate(ctx context.Context, api string, h *http.Client, clients map[string]*vaif.Client, in input) (any, error) {
	key := in.ProjectID + ":" + in.Actor
	c := clients[key]
	if c == nil {
		var err error
		c, err = vaif.NewClient(in.ProjectID, in.APIKey, vaif.WithBaseURL(api), vaif.WithHTTPClient(h), vaif.WithRetryPolicy(vaif.NoRetry()))
		if err != nil {
			return nil, err
		}
		clients[key] = c
		if in.Token != "" {
			c.Auth.SetSession(in.Token, "", time.Time{})
		}
	}
	a := in.Args
	s := func(k string) string { v, _ := a[k].(string); return v }
	q := c.From(in.Table)
	switch in.Op {
	case "signUp":
		r, e := c.Auth.SignUp(ctx, s("email"), s("password"), nil)
		if e != nil {
			return nil, e
		}
		if r.Session == nil {
			return nil, fmt.Errorf("verification pending")
		}
		return map[string]any{"accessToken": r.Session.AccessToken, "user": r.User}, nil
	case "setAuth":
		_, e := c.Auth.SetSession(s("token"), "", time.Time{})
		return map[string]any{}, e
	case "createRow":
		return q.Insert(ctx, a)
	case "listRows":
		r, e := q.List(ctx)
		if e != nil {
			return nil, e
		}
		return map[string]any{"data": r.Data, "count": r.Count}, nil
	case "getRow":
		return q.Get(ctx, s("id"))
	case "updateRow":
		return q.Update(ctx, s("id"), a["values"])
	case "deleteRow":
		return map[string]any{}, q.Delete(ctx, s("id"))
	case "storageUpload":
		return c.Storage.Upload(ctx, s("key"), []byte(s("text")), &vaif.UploadOptions{Bucket: in.Bucket, ContentType: "text/plain"})
	case "storageDownload":
		b, e := c.Storage.Download(ctx, s("key"), &vaif.ObjectOptions{Bucket: in.Bucket})
		return map[string]any{"text": string(b)}, e
	case "storageList":
		r, e := c.Storage.List(ctx, &vaif.ListObjectsOptions{Bucket: in.BucketID})
		if e != nil {
			return nil, e
		}
		return map[string]any{"files": r.Files}, nil
	case "invokeFunction":
		r, e := c.Functions.Invoke(ctx, s("name"), map[string]any{}, nil)
		if e != nil {
			return nil, e
		}
		var v any
		e = json.Unmarshal(r.Body, &v)
		return v, e
	case "realtimeRoundTrip":
		wait, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		sub, e := c.Realtime.Subscribe(wait, in.Table, nil)
		if e != nil {
			return nil, e
		}
		defer sub.Close()
		_, e = q.Insert(wait, vaif.Row{"body": "realtime conformance"})
		if e != nil {
			return nil, e
		}
		event, e := sub.Next(wait)
		if e != nil {
			return nil, e
		}
		return map[string]any{"op": event.Op}, nil
	default:
		return nil, fmt.Errorf("unknown operation")
	}
}
