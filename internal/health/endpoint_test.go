package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPEndpointRejectsServiceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	endpoint := Endpoint{Kind: "http", Target: server.URL + "/ready"}
	if err := endpoint.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	endpoint.Target = server.URL
	if err := endpoint.Probe(context.Background()); err == nil {
		t.Fatal("probe accepted HTTP 503")
	}
}
