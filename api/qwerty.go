package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Konfigurasi (ubah lewat env jika perlu)
var (
	XFORWARDER   = getEnv("XFORWARDER", "http://203.117.83.181:203")
	TARGET_HOST  = getEnv("TARGET_HOST", "ucdn.starhubgo.com")
	TARGET_SCHEME = getEnv("TARGET_SCHEME", "https")
	CORS_URL     = getEnv("CORS_URL", "http://cors-buster.fly.dev") // tanpa trailing slash
)

func main() {
	http.HandleFunc("/api/qwerty", gatewayHandler)   // akan dipanggil via rewrite /qwerty/(.*) -> /api/qwerty?path=$1
	http.HandleFunc("/api/qwerty/", gatewayHandler)  // fallback
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("UCdn Gateway (Go) — gunakan /qwerty/<path> via rewrite atau /api/qwerty?path=<path>\n"))
	})
	port := getEnv("PORT", "3000")
	log.Printf("Starting UCdn Gateway (Go) on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func gatewayHandler(w http.ResponseWriter, r *http.Request) {
	// Handle preflight
	if r.Method == http.MethodOptions {
		setCorsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Dapatkan path: prioritas dari query param "path" (dipakai oleh vercel rewrites), jika tidak ada coba dari URL path
	path := r.URL.Query().Get("path")
	if path == "" {
		// jika route memakai /api/qwerty/<rest> maka ambil sisanya
		// r.URL.Path bisa berupa "/api/qwerty/HubSports1HDnew1/output/manifest.mpd"
		prefix := "/api/qwerty/"
		if strings.HasPrefix(r.URL.Path, prefix) {
			path = strings.TrimPrefix(r.URL.Path, prefix)
		} else {
			// juga dukung kalau worker dipanggil langsung lewat /qwerty/ (jika tidak memakai rewrite)
			prefix2 := "/qwerty/"
			if strings.HasPrefix(r.URL.Path, prefix2) {
				path = strings.TrimPrefix(r.URL.Path, prefix2)
			}
		}
	}

	if path == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("UCdn Gateway — usage:\n - rewrite: /qwerty/<path>\n - or direct: /api/qwerty?path=<path>\nExample:\n /qwerty/HubSports1HDnew1/output/manifest.mpd\n"))
		return
	}

	// Build target URL
	targetUrl := fmt.Sprintf("%s://%s/%s", TARGET_SCHEME, TARGET_HOST, strings.TrimLeft(path, "/"))

	// Mode
	mode := strings.ToLower(r.URL.Query().Get("mode"))

	// Jika mode=proxy -> lakukan proxy via xforwarder
	if mode == "proxy" {
		proxyViaXForwarder(w, r, targetUrl)
		return
	}

	// Default: redirect ke cors-buster path-style
	redirectLocation := fmt.Sprintf("%s/%s", strings.TrimRight(CORS_URL, "/"), targetUrl)
	http.Redirect(w, r, redirectLocation, http.StatusFound) // 302
}

func proxyViaXForwarder(w http.ResponseWriter, r *http.Request, targetUrl string) {
	// Setup transport dengan proxy
	proxyURL, err := url.Parse(XFORWARDER)
	if err != nil {
		http.Error(w, "Invalid proxy URL", http.StatusInternalServerError)
		return
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		// Jika perlu disable TLS verify (tidak direkomendasikan), ubah TLSClientConfig:
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0, // no timeout, streaming
	}

	// Buat request baru ke upstream
	ctx := r.Context()
	upReq, err := http.NewRequestWithContext(ctx, r.Method, targetUrl, r.Body)
	if err != nil {
		http.Error(w, "Failed create upstream request", http.StatusInternalServerError)
		return
	}

	// Forward safe headers
	copySafeRequestHeaders(upReq.Header, r.Header)

	// Pastikan User-Agent
	if upReq.Header.Get("User-Agent") == "" {
		upReq.Header.Set("User-Agent", "UCdn-Gateway-Go/1.0")
	}

	// Do request
	upResp, err := client.Do(upReq)
	if err != nil {
		log.Printf("Upstream fetch error: %v\n", err)
		http.Error(w, "Upstream fetch error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// Forward status code
	w.WriteHeader(upResp.StatusCode)

	// Forward safe headers from upstream
	copySafeResponseHeaders(w.Header(), upResp.Header)

	// Tambahkan CORS headers supaya browser bisa request
	setCorsHeaders(w)

	// Stream body
	_, err = io.Copy(w, upResp.Body)
	if err != nil {
		// Jika client closed connection, cukup log
		log.Printf("Stream copy error: %v\n", err)
	}
}

// copySafeRequestHeaders menyalin header yang aman dari client ke upstream request
func copySafeRequestHeaders(dst http.Header, src http.Header) {
	for k, vv := range src {
		lk := strings.ToLower(k)
		// skip hop-by-hop and proxy related headers
		if lk == "host" || lk == "connection" || lk == "proxy-authorization" || lk == "proxy-authenticate" ||
			lk == "te" || lk == "trailer" || lk == "transfer-encoding" || lk == "upgrade" {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// copySafeResponseHeaders menyalin header tertentu dari upstream ke response
func copySafeResponseHeaders(dst http.Header, src http.Header) {
	safe := map[string]bool{
		"Content-Type":   true,
		"Content-Length": true,
		"Cache-Control":  true,
		"Etag":           true,
		"Last-Modified":  true,
		"Accept-Ranges":  true,
		"Content-Range":  true,
	}
	for k, vv := range src {
		if safe[k] {
			for _, v := range vv {
				dst.Add(k, v)
			}
		}
	}
}

func setCorsHeaders(w http.ResponseWriter) {
	// Kembalikan Origin sesuai kebutuhan, di sini "*" untuk lebih longgar
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Type, Accept-Ranges")
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
