package modeltest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
)

// Contains matches a URL-path substring. Empty Method or Contains matches any
// method or path, respectively.
type Route struct {
	Method   string
	Contains string
	Handle   http.HandlerFunc
}

// MuxServer matches routes in order; place a catch-all last.
// The caller owns server cleanup.
func MuxServer(routes ...Route) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, route := range routes {
			if route.Method != "" && route.Method != r.Method {
				continue
			}
			if route.Contains != "" && !strings.Contains(r.URL.Path, route.Contains) {
				continue
			}
			route.Handle(w, r)
			return
		}
		http.Error(w, "modeltest.MuxServer: no route matched "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
}

// PollCounter is safe for concurrent handlers.
type PollCounter struct {
	n atomic.Int32
}

func (p *PollCounter) Inc() int32 { return p.n.Add(1) }

func (p *PollCounter) N() int32 { return p.n.Load() }
