package main

import (
	"io"
	"log"
	"net/http"
	"strings"
)

// Handler akan dipanggil oleh Vercel
func Handler(w http.ResponseWriter, r *http.Request) {
	// Ambil path setelah /starhub/
	path := strings.TrimPrefix(r.URL.Path, "/starhub/")
	if path == "" {
		http.Error(w, "Path tidak boleh kosong", http.StatusBadRequest)
		return
	}

	// Buat URL target via CORS Buster
	targetURL := "https://cors-buster.fly.dev/https://ucdn.starhubgo.com/" + path

	proxyRequest(w, r, targetURL)
}

func proxyRequest(w http.ResponseWriter, r *http.Request, target string) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Ubah URL redirect tetap melalui CORS Buster
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			req.URL.Scheme = "https"
			req.URL.Host = "cors-buster.fly.dev"
			req.URL.Path = "/https://ucdn.starhubgo.com" + req.URL.Path
			req.Header.Set("User-Agent", "ExoPlayerDemo/2.15.1 (Linux; Android 13) ExoPlayerLib/2.15.1")
			req.Header.Set("X-Forwarded-For", "203.117.83.181")
			return nil
		},
	}

	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		http.Error(w, "Gagal membuat request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	req.Header.Set("User-Agent", "ExoPlayerDemo/2.15.1 (Linux; Android 13) ExoPlayerLib/2.15.1")
	req.Header.Set("X-Forwarded-For", "203.117.83.181")

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Gagal request target: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Salin header dari target ke response
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
