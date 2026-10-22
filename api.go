package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	cfg         *Config
	store       *Store
	limiter     *failLimiter
	adminCookie string // 后台隐藏开启时的解锁 cookie 值，每次启动随机
}

// ---------- 响应 ----------

type result struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

func ok(w http.ResponseWriter, msg string, data any) {
	if data == nil {
		data = struct{}{}
	}
	writeJSON(w, 200, result{true, msg, data})
}

// fail 与 lsky 一致：业务错误 HTTP 200 + status:false。
func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, result{false, msg, struct{}{}})
}

// ---------- 链接 ----------

type links struct {
	URL              string `json:"url"`
	HTML             string `json:"html"`
	BBCode           string `json:"bbcode"`
	Markdown         string `json:"markdown"`
	MarkdownWithLink string `json:"markdown_with_link"`
	ThumbnailURL     string `json:"thumbnail_url"`
	Webp             string `json:"webp"`
	WebpMarkdown     string `json:"webp_markdown"`
}

func (s *Server) links(m *Image) links {
	u := s.cfg.BaseURL + s.cfg.ImgPrefix + "/" + m.Pathname()
	wp := u
	if m.WebpSize > 0 {
		wp = s.cfg.BaseURL + s.cfg.WebpPrefix + "/" + webpRel(m)
	}
	n := displayName(m)
	return links{
		URL:              u,
		HTML:             fmt.Sprintf(`<img src="%s" alt="%s" title="%s" />`, u, n, n),
		BBCode:           "[img]" + u + "[/img]",
		Markdown:         "![" + n + "](" + u + ")",
		MarkdownWithLink: "[![" + n + "](" + u + ")](" + u + ")",
		ThumbnailURL:     wp,
		Webp:             wp,
		WebpMarkdown:     "![" + n + "](" + wp + ")",
	}
}

func displayName(m *Image) string {
	if m.OriginName != "" {
		return m.OriginName
	}
	return m.Name
}

func kb(n int64) float64 { return float64(n) / 1024 }

type uploadData struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	Pathname   string  `json:"pathname"`
	OriginName string  `json:"origin_name"`
	Size       float64 `json:"size"`
	Mimetype   string  `json:"mimetype"`
	Extension  string  `json:"extension"`
	MD5        string  `json:"md5"`
	SHA1       string  `json:"sha1"`
	Links      links   `json:"links"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	WebpSize   float64 `json:"webp_size"`
	Duplicate  bool    `json:"duplicate"`
}

type listItem struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	OriginName string  `json:"origin_name"`
	Size       float64 `json:"size"`
	Mimetype   string  `json:"mimetype"`
	Extension  string  `json:"extension"`
	MD5        string  `json:"md5"`
	SHA1       string  `json:"sha1"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	HumanDate  string  `json:"human_date"`
	Date       string  `json:"date"`
	Pathname   string  `json:"pathname"`
	Links      links   `json:"links"`
	WebpSize   float64 `json:"webp_size"`
}

func humanDate(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%d 个月前", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%d 年前", int(d.Hours()/24/365))
}

// ---------- 路由 ----------

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/upload", s.auth(s.handleUpload))
	mux.HandleFunc("GET /api/v1/images", s.auth(s.handleImages))
	mux.HandleFunc("DELETE /api/v1/images/{key}", s.auth(s.handleDelete))
	mux.HandleFunc("POST /api/v1/tokens", s.handleIssueToken)
	mux.HandleFunc("DELETE /api/v1/tokens", s.auth(s.handleClearTokens))
	mux.HandleFunc("GET /api/v1/profile", s.auth(s.handleProfile))
	mux.HandleFunc("GET /api/v1/strategies", s.auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, "success", map[string]any{"strategies": []map[string]any{{"id": 1, "name": "本地"}}})
	}))
	mux.HandleFunc("GET /api/v1/albums", s.auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, "success", s.page(r, 1, 40, 0, []any{}))
	}))

	mux.HandleFunc("GET /api/admin/tokens", s.auth(s.handleListTokens))
	mux.HandleFunc("POST /api/admin/tokens", s.auth(s.handleCreateToken))
	mux.HandleFunc("DELETE /api/admin/tokens/{id}", s.auth(s.handleRevokeToken))
	mux.HandleFunc("POST /api/admin/images/delete", s.auth(s.handleBatchDelete))
	mux.HandleFunc("POST /api/admin/download", s.auth(s.handleDownload))
	mux.HandleFunc("GET /api/admin/session", s.auth(func(w http.ResponseWriter, r *http.Request) {
		ok(w, "success", map[string]any{"base_url": s.cfg.BaseURL})
	}))

	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })

	// 生产环境由 OpenResty 直出，这里兜底与本地开发用。
	mux.Handle("GET "+s.cfg.ImgPrefix+"/", staticDir(s.cfg.ImgPrefix, s.cfg.Uploads))
	if s.cfg.ImgPrefix != "/i" {
		mux.Handle("GET /i/", staticDir("/i", s.cfg.Uploads))
	}
	mux.Handle("GET "+s.cfg.WebpPrefix+"/", staticDir(s.cfg.WebpPrefix, s.cfg.WebpDir))

	mux.Handle("GET /", webHandler())
	return s.gate(mux)
}

// ---------- 后台隐藏 ----------

const adminCookieName = "leopic_admin"
const adminCookieTTL = 30 * 24 * time.Hour

// isPublicPath 后台隐藏不影响图片直链与健康检查。
func (s *Server) isPublicPath(p string) bool {
	if p == "/healthz" {
		return true
	}
	for _, pre := range []string{s.cfg.ImgPrefix + "/", "/i/", s.cfg.WebpPrefix + "/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func (s *Server) adminUnlocked(r *http.Request) bool {
	c, err := r.Cookie(adminCookieName)
	return err == nil && constEq(c.Value, s.adminCookie)
}

// gate：设置 LEOPIC_ADMIN_PATH 后，访问该秘密路径种 cookie 解锁后台，
// 未解锁的页面与 API 一律 301 到 LEOPIC_ADMIN_REDIRECT（隐藏管理端存在）。
func (s *Server) gate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminPath == "" {
			h.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == s.cfg.AdminPath {
			http.SetCookie(w, &http.Cookie{
				Name: adminCookieName, Value: s.adminCookie, Path: "/", MaxAge: int(adminCookieTTL.Seconds()),
				HttpOnly: true, Secure: strings.HasPrefix(s.cfg.BaseURL, "https://"), SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if s.isPublicPath(r.URL.Path) || s.adminUnlocked(r) {
			h.ServeHTTP(w, r)
			return
		}
		// no-cache 防止浏览器把这次 301 钉死，解锁后仍能正常进入后台。
		w.Header().Set("Cache-Control", "no-cache")
		http.Redirect(w, r, s.cfg.AdminRedirect, http.StatusMovedPermanently)
	})
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(s.store.db, r) {
			fail(w, http.StatusUnauthorized, "Unauthenticated.")
			return
		}
		h(w, r)
	}
}

// ---------- 上传 ----------

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxSize+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, 200, errTooLarge.Error())
			return
		}
		fail(w, 200, "请选择要上传的图片")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 200, "请选择要上传的图片")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, s.cfg.MaxSize+1))
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	m, err := s.store.ingest(ingestReq{Data: data, OriginName: cleanName(hdr.Filename)})
	dup := errors.Is(err, errDuplicate)
	if err != nil && !dup {
		switch err {
		case errTooLarge, errTooMany, errUnsupported:
			fail(w, 200, err.Error())
		default:
			fmt.Fprintf(os.Stderr, "upload: %v\n", err)
			fail(w, 200, "服务异常，请稍后再试")
		}
		return
	}
	ok(w, "上传成功", uploadData{
		Key: m.Key, Name: m.Name, Pathname: m.Pathname(), OriginName: m.OriginName,
		Size: kb(m.Size), Mimetype: m.Mimetype, Extension: m.Extension, MD5: m.MD5, SHA1: m.SHA1,
		Links: s.links(m), Width: m.Width, Height: m.Height, WebpSize: kb(m.WebpSize), Duplicate: dup,
	})
}

func cleanName(n string) string {
	n = strings.ToValidUTF8(strings.ReplaceAll(n, "\\", "/"), "")
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	n = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, n)
	if r := []rune(n); len(r) > 200 {
		n = string(r[:200])
	}
	return n
}

// ---------- 列表 ----------

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per < 1 || per > 100 {
		per = 40
	}
	order := map[string]string{
		"earliest": "created_at ASC, id ASC",
		"utmost":   "size DESC, id DESC",
		"least":    "size ASC, id ASC",
	}[q.Get("order")]
	if order == "" {
		order = "created_at DESC, id DESC"
	}
	where, args := "", []any{}
	if kw := strings.TrimSpace(q.Get("keyword")); kw != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(kw) + "%"
		where = ` WHERE origin_name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR key LIKE ? ESCAPE '\'`
		args = append(args, like, like, like)
	}
	var total int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM images"+where, args...).Scan(&total); err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	rows, err := s.store.db.Query("SELECT "+imageCols+" FROM images"+where+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, per, (page-1)*per)...)
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		m, err := scanImage(rows)
		if err != nil {
			fail(w, 200, "服务异常，请稍后再试")
			return
		}
		t := time.Unix(m.CreatedAt, 0).In(shanghai)
		items = append(items, listItem{
			Key: m.Key, Name: m.Name, OriginName: m.OriginName, Size: kb(m.Size), Mimetype: m.Mimetype,
			Extension: m.Extension, MD5: m.MD5, SHA1: m.SHA1, Width: m.Width, Height: m.Height,
			HumanDate: humanDate(t), Date: t.Format("2006-01-02 15:04:05"), Pathname: m.Pathname(),
			Links: s.links(m), WebpSize: kb(m.WebpSize),
		})
	}
	ok(w, "success", s.page(r, page, per, total, items))
}

// page 生成 Laravel LengthAwarePaginator 风格的分页结构。
func (s *Server) page(r *http.Request, page, per, total int, items []any) map[string]any {
	last := (total + per - 1) / per
	if last < 1 {
		last = 1
	}
	base := s.cfg.BaseURL + r.URL.Path
	pageURL := func(p int) any {
		if p < 1 || p > last {
			return nil
		}
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		return base + "?" + q.Encode()
	}
	var from, to any
	if len(items) > 0 {
		from = (page-1)*per + 1
		to = (page-1)*per + len(items)
	}
	return map[string]any{
		"current_page":   page,
		"data":           items,
		"first_page_url": pageURL(1),
		"from":           from,
		"last_page":      last,
		"last_page_url":  pageURL(last),
		"next_page_url":  pageURL(page + 1),
		"path":           base,
		"per_page":       per,
		"prev_page_url":  pageURL(page - 1),
		"to":             to,
		"total":          total,
	}
}

// ---------- 删除 / profile ----------

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.delete(r.PathValue("key")); err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	ok(w, "删除成功", nil)
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	var n int
	var used int64
	s.store.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size),0) FROM images").Scan(&n, &used)
	ok(w, "success", map[string]any{
		"name": "leopic", "avatar": "", "email": s.cfg.Email, "capacity": 0,
		"used_capacity": kb(used), "url": s.cfg.BaseURL, "image_num": n, "album_num": 0, "registered_ip": "",
	})
}

// ---------- token ----------

func readParams(r *http.Request) url.Values {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&m)
		v := url.Values{}
		for k, x := range m {
			if s, ok := x.(string); ok {
				v.Set(k, s)
			}
		}
		return v
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<16)
	r.ParseForm()
	return r.Form
}

func (s *Server) checkPassword(w http.ResponseWriter, email, password string, checkEmail bool) bool {
	if !s.limiter.allow() {
		fail(w, http.StatusTooManyRequests, errLimited.Error())
		return false
	}
	good := constEq(password, s.cfg.Password)
	if checkEmail && s.cfg.Email != "" && !strings.EqualFold(strings.TrimSpace(email), s.cfg.Email) {
		good = false
	}
	if !good {
		s.limiter.fail()
	}
	return good
}

func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	p := readParams(r)
	if !s.checkPassword(w, p.Get("email"), p.Get("password"), true) {
		if w.Header().Get("Content-Type") == "" {
			fail(w, 200, "The email address or password is incorrect.")
		}
		return
	}
	tok, err := createToken(s.store.db, p.Get("email"))
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	ok(w, "success", map[string]string{"token": tok})
}

func (s *Server) handleClearTokens(w http.ResponseWriter, r *http.Request) {
	s.store.db.Exec("DELETE FROM tokens")
	ok(w, "success", nil)
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.Query("SELECT id, name, created_at, last_used_at FROM tokens ORDER BY id")
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, created int64
		var name string
		var last *int64
		if rows.Scan(&id, &name, &created, &last) == nil {
			list = append(list, map[string]any{"id": id, "name": name, "created_at": created, "last_used_at": last})
		}
	}
	ok(w, "success", list)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(readParams(r).Get("name"))
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64])
	}
	tok, err := createToken(s.store.db, name)
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	ok(w, "success", map[string]string{"token": tok})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	s.store.db.Exec("DELETE FROM tokens WHERE id=?", r.PathValue("id"))
	ok(w, "success", nil)
}

// ---------- 登录 ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	p := readParams(r)
	if !s.checkPassword(w, "", p.Get("password"), false) {
		if w.Header().Get("Content-Type") == "" {
			fail(w, 200, "密码错误")
		}
		return
	}
	v, err := createSession(s.store.db)
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: v, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: strings.HasPrefix(s.cfg.BaseURL, "https://"), SameSite: http.SameSiteStrictMode,
	})
	ok(w, "success", nil)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	deleteSession(s.store.db, r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	ok(w, "success", nil)
}

// ---------- 静态 ----------

// staticDir 只出文件，不列目录。
func staticDir(prefix, dir string) http.Handler {
	fs := http.StripPrefix(prefix, http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
		fs.ServeHTTP(w, r)
	})
}
