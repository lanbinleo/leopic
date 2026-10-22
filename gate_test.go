package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func gateServer(adminPath, redirect string) http.Handler {
	s := &Server{
		cfg:         &Config{AdminPath: adminPath, AdminRedirect: redirect, ImgPrefix: "/img", WebpPrefix: "/webp"},
		adminCookie: "test-cookie",
	}
	return s.gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("app"))
	}))
}

func TestGateOffPassesEverything(t *testing.T) {
	h := gateServer("", "")
	for _, path := range []string{"/", "/login", "/api/v1/images", "/healthz", "/img/a.png"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("未开启隐藏时 %s 应直通, got %d", path, w.Code)
		}
	}
}

func TestGateHidesAndUnlocks(t *testing.T) {
	h := gateServer("/s3cret-knock", "https://example.com")

	// 未解锁：页面与 API 一律 301，图片直链与 healthz 不受影响
	checks := map[string]int{
		"/":                 301,
		"/login":            301,
		"/api/v1/images":    301,
		"/healthz":          200,
		"/img/2026/a.png":   200,
		"/i/2026/a.png":     200,
		"/webp/2026/a.webp": 200,
		"/s3cret-knock":     302,
		"/app.js":           301,
	}
	for path, want := range checks {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", path, w.Code, want)
		}
		if want == 301 && w.Header().Get("Location") != "https://example.com" {
			t.Fatalf("%s: 301 目标 %q", path, w.Header().Get("Location"))
		}
	}

	// 敲门：种 cookie 后跳 /
	r := httptest.NewRequest("GET", "/s3cret-knock", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Name != adminCookieName || c[0].Value != "test-cookie" || !c[0].HttpOnly {
		t.Fatalf("敲门应种 HttpOnly cookie: %+v", c)
	}

	// 带 cookie：后台恢复可见，且 301 不被缓存
	for _, path := range []string{"/", "/api/v1/images", "/app.js"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "test-cookie"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("解锁后 %s 应 200, got %d", path, w.Code)
		}
	}

	// 错误 cookie 不放行
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: "wrong"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 301 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("错误 cookie 应 301 + no-cache, got %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestGateCookieTTL(t *testing.T) {
	if adminCookieTTL != 30*24*time.Hour {
		t.Fatal("后台解锁 cookie 应 30 天")
	}
}
