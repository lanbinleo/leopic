package main

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxBatch = 1000

// deleteMany 在一个事务里删除记录，提交后再删文件。返回实际删除数。
func (s *Store) deleteMany(keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var gone []*Image
	for _, k := range keys {
		m, err := scanImage(tx.QueryRow("SELECT "+imageCols+" FROM images WHERE key=?", k))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec("DELETE FROM images WHERE id=?", m.ID); err != nil {
			return 0, err
		}
		gone = append(gone, m)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, m := range gone {
		os.Remove(s.originalPath(m))
		os.Remove(s.webpPath(m))
		if m.CleanName != "" {
			os.Remove(s.cleanPath(m))
		}
	}
	return len(gone), nil
}

// byKeys 按传入顺序返回存在的记录（去重）。
func (s *Store) byKeys(keys []string) ([]*Image, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	rows, err := s.db.Query("SELECT "+imageCols+" FROM images WHERE key IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]*Image{}
	for rows.Next() {
		m, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		found[m.Key] = m
	}
	var out []*Image
	for _, k := range keys {
		if m := found[k]; m != nil {
			out = append(out, m)
			delete(found, k)
		}
	}
	return out, rows.Err()
}

func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.Keys) == 0 {
		fail(w, 200, "请选择要删除的图片")
		return
	}
	if len(body.Keys) > maxBatch {
		fail(w, 200, fmt.Sprintf("一次最多删除 %d 张", maxBatch))
		return
	}
	n, err := s.store.deleteMany(body.Keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "batch delete: %v\n", err)
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	ok(w, "删除成功", map[string]int{"deleted": n})
}

// downloadName 用原始文件名；WebP 版本换扩展名。去掉文件系统不允许的字符。
func downloadName(m *Image, webp bool) string {
	n := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, cleanName(m.OriginName))
	n = strings.Trim(n, " .")
	if n == "" {
		n = m.Name
	}
	ext := filepath.Ext(n)
	if webp {
		return strings.TrimSuffix(n, ext) + ".webp"
	}
	if ext == "" {
		n += "." + m.Extension
	}
	return n
}

// uniqueNamer 重名时追加 " (2)"、" (3)"，按 Windows 习惯不区分大小写。
type uniqueNamer map[string]bool

func (u uniqueNamer) take(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	cand := name
	for i := 2; u[strings.ToLower(cand)]; i++ {
		cand = stem + " (" + strconv.Itoa(i) + ")" + ext
	}
	u[strings.ToLower(cand)] = true
	return cand
}

func attachment(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(name))
}

// handleDownload：表单 POST，字段 key（可重复）与 variant（original | webp | clean）。
// clean 只打包已生成无水印版的图，其余回落带水印的 WebP。
// 单张直接返回文件，多张流式打包为 zip（Store 不压缩，图片本身已压缩）。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		fail(w, 200, "参数错误")
		return
	}
	keys := r.PostForm["key"]
	if len(keys) == 0 || len(keys) > maxBatch {
		fail(w, 200, fmt.Sprintf("请选择 1 到 %d 张图片", maxBatch))
		return
	}
	variant := r.PostForm.Get("variant")
	webp := variant == "webp" || variant == "clean"
	list, err := s.store.byKeys(keys)
	if err != nil || len(list) == 0 {
		fail(w, 200, "图片不存在")
		return
	}

	type entry struct {
		path, name string
		mod        time.Time
	}
	names := uniqueNamer{}
	var entries []entry
	for _, m := range list {
		p, n := s.store.originalPath(m), downloadName(m, false)
		if variant == "clean" && m.CleanName != "" {
			p, n = s.store.cleanPath(m), downloadName(m, true)
		} else if webp && m.WebpSize > 0 {
			p, n = s.store.webpPath(m), downloadName(m, true)
		}
		entries = append(entries, entry{p, names.take(n), time.Unix(m.CreatedAt, 0)})
	}

	if len(entries) == 1 {
		e := entries[0]
		f, err := os.Open(e.path)
		if err != nil {
			fail(w, 200, "文件不存在")
			return
		}
		defer f.Close()
		w.Header().Set("Content-Disposition", attachment(e.name))
		http.ServeContent(w, r, e.name, e.mod, f)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachment(time.Now().In(shanghai).Format("leopic-20060102-150405.zip")))
	zw := zip.NewWriter(w)
	for _, e := range entries {
		f, err := os.Open(e.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "download: %v\n", err)
			continue
		}
		hw, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: zip.Store, Modified: e.mod.In(shanghai)})
		if err == nil {
			_, err = io.Copy(hw, f)
		}
		f.Close()
		if err != nil { // 多半是客户端断开
			return
		}
	}
	zw.Close()
}
