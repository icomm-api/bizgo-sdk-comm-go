// Package testserver is a scripted mock of the Bizgo API for the SDK's offline tests and examples.
package testserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// APIKey is the placeholder key used by all tests.
const APIKey = "test-api-key-not-real"

// Reply is one scripted response.
type Reply struct {
	Status     int
	Header     http.Header
	Body       string
	Disconnect bool // close the connection without a response
	Hang       bool // wait until the client gives up (timeout)
}

// JSON returns a reply with a JSON body.
func JSON(status int, v any) Reply {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Reply{Status: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: string(b)}
}

// Text returns a reply with a plain body.
func Text(status int, body string) Reply { return Reply{Status: status, Body: body} }

// Envelope builds a {common, data} body. Code "" means A000.
func Envelope(data any, code string, ref string) map[string]any {
	if code == "" {
		code = "A000"
	}
	result := "Success"
	if code != "A000" {
		result = "Failed"
	}
	d := map[string]any{"code": code, "result": result}
	if data != nil {
		d["data"] = data
	}
	if ref != "" {
		d["ref"] = ref
	}
	return map[string]any{
		"common": map[string]any{"authCode": "A000", "authResult": "Success", "infobankTrId": "TR-TEST"},
		"data":   d,
	}
}

// OK is an HTTP 200 reply with Envelope(data, "", "").
func OK(data any) Reply { return JSON(200, Envelope(data, "", "")) }

// Call is a recorded request.
type Call struct {
	Method   string
	Path     string // escaped path as sent
	RawQuery string
	Query    url.Values
	Header   http.Header
	Body     []byte
}

// Route is a scripted endpoint. Replies are used in order; the last one repeats.
type Route struct {
	mu      sync.Mutex
	replies []Reply
	calls   []Call
}

// Calls returns the recorded requests.
func (r *Route) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Call(nil), r.calls...)
}

// Count returns the number of requests received.
func (r *Route) Count() int { return len(r.Calls()) }

// Last returns the last request (it fails the test if there is none).
func (r *Route) Last(t testing.TB) Call {
	t.Helper()
	calls := r.Calls()
	if len(calls) == 0 {
		t.Fatal("route was not called")
	}
	return calls[len(calls)-1]
}

// Server is an httptest server with scripted routes. Unknown routes answer 599.
type Server struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]*Route
	other  []Call
}

// New starts a server that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{routes: map[string]*Route{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// On scripts METHOD path (escaped path, without query).
func (s *Server) On(method, path string, replies ...Reply) *Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &Route{replies: replies}
	s.routes[method+" "+path] = r
	return r
}

// Unrouted returns requests that matched no route.
func (s *Server) Unrouted() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.other...)
}

func (s *Server) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	call := Call{
		Method: req.Method, Path: req.URL.EscapedPath(), RawQuery: req.URL.RawQuery,
		Query: req.URL.Query(), Header: req.Header.Clone(), Body: body,
	}
	s.mu.Lock()
	route := s.routes[req.Method+" "+call.Path]
	if route == nil {
		s.other = append(s.other, call)
	}
	s.mu.Unlock()
	if route == nil {
		http.Error(w, "no route", 599)
		return
	}
	route.mu.Lock()
	route.calls = append(route.calls, call)
	var reply Reply
	if n := len(route.calls); len(route.replies) > 0 {
		reply = route.replies[min(n, len(route.replies))-1]
	} else {
		reply = OK(nil)
	}
	route.mu.Unlock()

	switch {
	case reply.Disconnect:
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	case reply.Hang:
		select {
		case <-req.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return
	}
	for k, v := range reply.Header {
		w.Header()[k] = v
	}
	status := reply.Status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, reply.Body)
}
