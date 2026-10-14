package main

import (
	"embed"
	"net/http"
)

//go:embed web/*
var webAssets embed.FS

func serveEmbedded(w http.ResponseWriter, path, contentType string) {
	data, err := webAssets.ReadFile("web/" + path)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
func handleLoginPage(w http.ResponseWriter, req *http.Request) {
	serveEmbedded(w, "login.html", "text/html; charset=utf-8")
}
func handleIndex(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	serveEmbedded(w, "index.html", "text/html; charset=utf-8")
}
func handleAsset(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/assets/style.css":
		serveEmbedded(w, "style.css", "text/css; charset=utf-8")
	case "/assets/index.js":
		serveEmbedded(w, "index.js", "application/javascript; charset=utf-8")
	case "/assets/login.js":
		serveEmbedded(w, "login.js", "application/javascript; charset=utf-8")
	default:
		http.NotFound(w, req)
	}
}
