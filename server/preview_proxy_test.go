package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreviewProxy(t *testing.T) {
	port := startPreviewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/clear":
			w.Header().Set("Clear-Site-Data", `"*"`)
		case "/loop":
			fmt.Fprint(w, r.Header.Get(previewHeader))
		default:
			fmt.Fprintf(w, "%s?%s", r.URL.Path, r.URL.RawQuery)
		}
	}))

	proxy := httptest.NewServer(previewMux(http.NotFoundHandler()))
	defer proxy.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, test := range []struct {
		name         string
		path         string
		wantBody     string
		wantLocation string
	}{
		{"path and query", fmt.Sprintf("/__preview/%d/hello/world?name=ada", port), "/hello/world?name=ada", ""},
		{"location", fmt.Sprintf("/__preview/%d/redirect", port), "", fmt.Sprintf("/__preview/%d/login", port)},
		{"marks upstream requests", fmt.Sprintf("/__preview/%d/loop", port), "1", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := client.Get(proxy.URL + test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if test.wantLocation != "" && response.Header.Get("Location") != test.wantLocation {
				t.Errorf("Location = %q, want %q", response.Header.Get("Location"), test.wantLocation)
			}
			if response.Header.Get("Clear-Site-Data") != "" {
				t.Error("Clear-Site-Data forwarded")
			}
			if test.wantBody != "" {
				var got string
				if _, err := fmt.Fscan(response.Body, &got); err != nil {
					t.Fatal(err)
				}
				if got != test.wantBody {
					t.Errorf("body = %q, want %q", got, test.wantBody)
				}
			}
		})
	}
}

func TestPreviewRefererMiddleware(t *testing.T) {
	port := startPreviewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	}))

	proxy := httptest.NewServer(PreviewRefererMiddleware(previewMux(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fallback")
	}))))
	defer proxy.Close()

	request, err := http.NewRequest(http.MethodGet, proxy.URL+"/assets/app.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Referer", fmt.Sprintf("%s/__preview/%d/", proxy.URL, port))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var got string
	if _, err := fmt.Fscan(response.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got != "/assets/app.js" {
		t.Errorf("body = %q, want %q", got, "/assets/app.js")
	}
	if want := fmt.Sprintf("/__preview/%d/assets/app.js", port); response.Request.URL.Path != want {
		t.Errorf("final URL = %q, want %q", response.Request.URL.Path, want)
	}

	for name, referer := range map[string]string{
		"no referer":      "",
		"foreign referer": fmt.Sprintf("http://evil.example/__preview/%d/", port),
	} {
		request, err := http.NewRequest(http.MethodGet, proxy.URL+"/assets/app.js", nil)
		if err != nil {
			t.Fatal(err)
		}
		if referer != "" {
			request.Header.Set("Referer", referer)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if _, err := fmt.Fscan(response.Body, &got); err != nil {
			t.Fatal(err)
		}
		if got != "fallback" {
			t.Errorf("%s: body = %q, want fallback", name, got)
		}
	}
}

func TestPreviewProxyErrors(t *testing.T) {
	proxy := httptest.NewServer(PreviewRefererMiddleware(previewMux(http.NotFoundHandler())))
	defer proxy.Close()
	closedPort := closedPreviewPort(t)

	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{"bad port", "/__preview/2999/", http.StatusBadRequest},
		{"self loop", "/version", http.StatusBadRequest},
		{"closed port", fmt.Sprintf("/__preview/%d/", closedPort), http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, proxy.URL+test.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "self loop" {
				request.Header.Set(previewHeader, "1")
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Errorf("status = %d, want %d", response.StatusCode, test.want)
			}
		})
	}
}

func previewMux(fallback http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(previewPrefix, previewProxyHandler())
	mux.Handle("/", fallback)
	return mux
}

func startPreviewUpstream(t *testing.T, handler http.Handler) int {
	t.Helper()
	for port := 3000; port <= 9999; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		server := httptest.NewUnstartedServer(handler)
		server.Listener = listener
		server.Start()
		t.Cleanup(server.Close)
		return port
	}
	t.Fatal("no preview port available")
	return 0
}

func closedPreviewPort(t *testing.T) int {
	t.Helper()
	for port := 3000; port <= 9999; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return port
	}
	t.Fatal("no preview port available")
	return 0
}
