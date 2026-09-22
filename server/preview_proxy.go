package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const (
	previewPrefix = "/__preview/"
	previewHeader = "X-Shelley-Preview"
)

func previewProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		port, path, ok := previewTarget(r.URL.Path)
		if !ok {
			http.Error(w, "invalid preview port", http.StatusBadRequest)
			return
		}

		target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = path
			if r.URL.RawPath == "" {
				req.URL.RawPath = ""
			} else {
				req.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, previewPrefix+port)
			}
			req.Host = target.Host
			req.Header.Set(previewHeader, "1")
		}
		proxy.ModifyResponse = func(response *http.Response) error {
			if location := response.Header.Get("Location"); strings.HasPrefix(location, "/") {
				response.Header.Set("Location", previewPrefix+port+location)
			}
			response.Header.Del("Clear-Site-Data")
			return nil
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "preview upstream unavailable", http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	})
}

func previewTarget(path string) (port, upstreamPath string, ok bool) {
	rest := strings.TrimPrefix(path, previewPrefix)
	port, upstreamPath, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", false
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 3000 || value > 9999 {
		return "", "", false
	}
	return port, "/" + upstreamPath, true
}

// PreviewRefererMiddleware redirects absolute-path requests made from a
// previewed page (identified by its Referer) back under that page's
// /__preview/<port>/ prefix, so the frame never leaves the proxy.
func PreviewRefererMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(previewHeader) != "" {
			http.Error(w, "preview cannot proxy itself", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(r.URL.Path, previewPrefix) {
			if referer, err := url.Parse(r.Referer()); err == nil && referer.Host == r.Host {
				if port, _, ok := previewTarget(referer.Path); ok {
					http.Redirect(w, r, previewPrefix+port+r.URL.RequestURI(), http.StatusTemporaryRedirect)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
