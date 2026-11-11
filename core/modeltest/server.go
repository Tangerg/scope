package modeltest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Route answers requests whose method equals Method and whose URL path
// contains Contains; an empty Method or Contains matches any method or path.
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

// JSONServer answers every request with body and status. Inspections run
// before the response is written and receive the live request; read the body
// during inspection, because it is unavailable after the handler returns.
func JSONServer(status int, body string, inspections ...func(request *http.Request)) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, inspect := range inspections {
			inspect(request)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprint(writer, body)
	}))
	return server
}

// BinaryServer is JSONServer for a non-JSON body of the given content type.
func BinaryServer(status int, contentType string, body []byte, inspections ...func(request *http.Request)) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, inspect := range inspections {
			inspect(request)
		}
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(status)
		_, _ = writer.Write(body)
	}))
	return server
}

// NewBlockingServer holds one request until cancellation. Lifecycle signals
// allow tests to synchronize with the initial write and handler exit.
func NewBlockingServer(t *testing.T, writeInitial func(http.ResponseWriter)) (*httptest.Server, Lifecycle) {
	t.Helper()
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(stopped)
		if writeInitial != nil {
			writeInitial(writer)
		} else {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
		}
		close(started)
		<-request.Context().Done()
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server, Lifecycle{Started: started, Stopped: stopped}
}
