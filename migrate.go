package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type importStats struct {
	imported, skipped, missing, failed, orphans, tokens, dropped int
	seen                                                         map[string]bool // dry-run 下记录已处理的路径
}

// lskyTime 解析 lsky 的 created_at：'Y-m-d H:i:s' 的 PRC 本地时间。
// modernc 驱动对 datetime 列可能直接给 time.Time（按 UTC 解读），这里统一按上海墙钟时间重建。
func lskyTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, shanghai)
	case string:
		if p, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(t), shanghai); err == nil {
			return p
		}
	case []byte:
		return lskyTime(string(t))
	}
	return time.Time{}
}

func str(v sql.NullString) string { return strings.TrimSpace(v.String) }

func (s *Store) exists(path, name string) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM images WHERE path=? AND name=?", path, name).Scan(&n)
	return n > 0, err
}

type lskyRow struct {
	key, path, name, display string
	created                  time.Time
	w, h                     int
}

// pathname 是相对上传根目录的路径。
func (r lskyRow) pathname() string { return strings.TrimPrefix(r.path+"/"+r.name, "/") }

func importLsky(s *Store, lskyDB string, dryRun, orphans, drop bool, mapFile string) (*importStats, error) {
	src, err := sql.Open("sqlite", "file:"+filepath.ToSlash(lskyDB)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer src.Close()

	rows, err := src.Query("SELECT key, path, name, origin_name, alias_name, width, height, created_at FROM images ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("读取 lsky images 表: %w", err)
	}
	// 按 (path,name) 分组，只保留最早一行（lsky 文件级去重，多行可指向同一文件）。
	seen := map[string]bool{}
	var list []lskyRow
	for rows.Next() {
		var key, path, name, origin, alias sql.NullString
		var w, h int
		var created any
		if err := rows.Scan(&key, &path, &name, &origin, &alias, &w, &h, &created); err != nil {
			rows.Close()
			return nil, err
		}
		r := lskyRow{key: str(key), path: strings.Trim(str(path), "/"), name: str(name), display: str(alias), created: lskyTime(created), w: w, h: h}
		if r.display == "" {
			r.display = str(origin)
		}
		id := r.path + "/" + r.name
		if r.name == "" || seen[id] {
			continue
		}
		seen[id] = true
		list = append(list, r)
	}
	rows.Close()

	st := &importStats{seen: map[string]bool{}}
	if drop {
		var pairs []compressedPair
		list, pairs = splitCompressed(list)
		st.dropped = len(pairs)
		if mapFile != "" {
			if err := writeRedirectMap(s.cfg, pairs, mapFile); err != nil {
				return st, err
			}
		}
	}

	for _, r := range list {
		s.importOne(st, ingestReq{OriginName: r.display, CreatedAt: r.created, Key: r.key, Path: r.path, Name: r.name}, dryRun)
	}

	if orphans {
		if err := s.importOrphans(st, dryRun); err != nil {
			return st, err
		}
	}

	if err := s.importTokens(st, src, dryRun); err != nil {
		return st, err
	}
	return st, nil
}

// ---------- 压缩图剔除 ----------

// compressedPair 是一对"压缩图 -> 原图"（utools-lsky 1.0 的 -compressed 双上传产物）。
type compressedPair struct {
	comp, orig lskyRow
}

var compressedName = regexp.MustCompile(`(?i)^(.+)-compressed\.(jpg|jpeg|png|webp|gif)$`)

// uniqidSec 取 lsky 13 位 hex 文件名内嵌的上传秒（uniqid 前 8 位）。
func uniqidSec(name string) int64 {
	if len(name) >= 8 {
		if sec, err := strconv.ParseInt(name[:8], 16, 64); err == nil {
			return sec
		}
	}
	return 0
}

// splitCompressed 把列表分成：保留项，以及"可安全剔除"的压缩图配对。
// 配对规则：显示名 stem 相同（去掉 -compressed.ext）+ 同日期目录 + 同宽高；
// 同名多候选时按文件名内嵌的上传时间就近消歧（60 秒内）。
// 无原图的压缩图（原图上传失败/已删）保留不剔——它们是唯一副本。
func splitCompressed(list []lskyRow) (kept []lskyRow, pairs []compressedPair) {
	byStem := map[string][]lskyRow{}
	dropped := map[string]bool{}
	for _, r := range list {
		if compressedName.MatchString(r.display) {
			continue
		}
		stem := r.display
		if i := strings.LastIndexByte(stem, '.'); i > 0 {
			stem = stem[:i]
		}
		byStem[stem] = append(byStem[stem], r)
	}
	for _, c := range list {
		m := compressedName.FindStringSubmatch(c.display)
		if m == nil {
			continue
		}
		sameDir := func(o lskyRow) bool { return o.path == c.path }
		sameDim := func(o lskyRow) bool { return o.w > 0 && o.w == c.w && o.h == c.h }
		cands := []lskyRow{}
		for _, o := range byStem[m[1]] {
			if sameDir(o) && sameDim(o) {
				cands = append(cands, o)
			}
		}
		if len(cands) > 1 {
			// 同名原图多张（如通名 image.png）：取上传时间最近的
			cs := uniqidSec(c.name)
			best, bestD := cands[0], int64(1<<62)
			for _, o := range cands {
				d := uniqidSec(o.name) - cs
				if d < 0 {
					d = -d
				}
				if d < bestD {
					bestD, best = d, o
				}
			}
			if bestD > 60 {
				continue // 消歧失败，保留压缩图
			}
			cands = []lskyRow{best}
		}
		if len(cands) == 0 {
			// 放宽到同目录唯一原图
			for _, o := range byStem[m[1]] {
				if sameDir(o) {
					cands = append(cands, o)
				}
			}
			if len(cands) != 1 {
				continue // 无原图或仍歧义，保留
			}
		}
		pairs = append(pairs, compressedPair{comp: c, orig: cands[0]})
		dropped[c.pathname()] = true
	}
	for _, r := range list {
		if !dropped[r.pathname()] {
			kept = append(kept, r)
		}
	}
	return kept, pairs
}

// writeRedirectMap 生成 nginx map：旧压缩图直链 -> 原图衍生图（GIF 无衍生图回落原图）。
func writeRedirectMap(cfg *Config, pairs []compressedPair, path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# 由 leopic import --drop-compressed 生成：旧 -compressed 直链 -> 原图衍生图\n")
	fmt.Fprintf(&b, "# 共 %d 条。用法：http 级 include 本文件，location /img/ 内\n", len(pairs))
	fmt.Fprintf(&b, "#   if ($leopic_redirect) { return 301 $leopic_redirect; }\n")
	b.WriteString("map $uri $leopic_redirect {\n    default \"\";\n")
	for _, p := range pairs {
		target := cfg.WebpPrefix + "/" + strings.TrimSuffix(p.orig.pathname(), filepath.Ext(p.orig.name)) + ".webp"
		if strings.EqualFold(filepath.Ext(p.orig.name), ".gif") {
			target = cfg.ImgPrefix + "/" + p.orig.pathname()
		}
		fmt.Fprintf(&b, "    %s/%s  %s;\n", cfg.ImgPrefix, p.comp.pathname(), target)
	}
	b.WriteString("}\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func (s *Store) importOne(st *importStats, r ingestReq, dryRun bool) {
	label := strings.TrimPrefix(r.Path+"/"+r.Name, "/")
	if st.seen[label] {
		st.skipped++
		return
	}
	st.seen[label] = true
	if ok, err := s.exists(r.Path, r.Name); err != nil {
		fmt.Fprintf(os.Stderr, "失败 %s: %v\n", label, err)
		st.failed++
		return
	} else if ok {
		st.skipped++
		return
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.Uploads, filepath.FromSlash(label)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "缺失 %s\n", label)
		st.missing++
		return
	}
	if dryRun {
		if _, err := sniff(data); err != nil {
			fmt.Fprintf(os.Stderr, "失败 %s: %v\n", label, err)
			st.failed++
			return
		}
		st.imported++
		return
	}
	r.Data = data
	m, err := s.ingest(r)
	switch {
	case errors.Is(err, errDuplicate):
		fmt.Fprintf(os.Stderr, "跳过 %s: 内容与 %s 相同（直链不受影响）\n", label, m.Pathname())
		st.skipped++
	case err != nil:
		fmt.Fprintf(os.Stderr, "失败 %s: %v\n", label, err)
		st.failed++
	default:
		st.imported++
	}
}

var uniqidName = regexp.MustCompile(`^([0-9a-f]{8})[0-9a-f]{5}\.`)

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true}

func (s *Store) importOrphans(st *importStats, dryRun bool) error {
	return filepath.WalkDir(s.cfg.Uploads, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !imageExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		rel, err := filepath.Rel(s.cfg.Uploads, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir, name := "", rel
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			dir, name = rel[:i], rel[i+1:]
		}
		if ok, err := s.exists(dir, name); err != nil || ok || st.seen[rel] {
			return err
		}
		created := time.Time{}
		if m := uniqidName.FindStringSubmatch(name); m != nil {
			if sec, err := strconv.ParseInt(m[1], 16, 64); err == nil {
				created = time.Unix(sec, 0)
			}
		}
		if created.IsZero() {
			if info, err := d.Info(); err == nil {
				created = info.ModTime()
			}
		}
		before := st.imported
		s.importOne(st, ingestReq{OriginName: name, CreatedAt: created, Path: dir, Name: name}, dryRun)
		if st.imported > before {
			st.orphans++
		}
		return nil
	})
}

func (s *Store) importTokens(st *importStats, src *sql.DB, dryRun bool) error {
	rows, err := src.Query("SELECT id, name, token, created_at, last_used_at FROM personal_access_tokens")
	if err != nil {
		fmt.Fprintf(os.Stderr, "未导入 token: %v\n", err)
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, token sql.NullString
		var created, used any
		if err := rows.Scan(&id, &name, &token, &created, &used); err != nil {
			return err
		}
		if str(token) == "" {
			continue
		}
		var lastUsed any
		if t := lskyTime(used); !t.IsZero() {
			lastUsed = t.Unix()
		}
		ct := lskyTime(created)
		if ct.IsZero() {
			ct = time.Now()
		}
		if dryRun {
			st.tokens++
			continue
		}
		res, err := s.db.Exec("INSERT OR IGNORE INTO tokens (id, name, token_hash, created_at, last_used_at) VALUES (?,?,?,?,?)",
			id, str(name), str(token), ct.Unix(), lastUsed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			st.tokens++
		}
	}
	return rows.Err()
}
