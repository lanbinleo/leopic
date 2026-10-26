package main

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

var shanghai = mustLoc("Asia/Shanghai")

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

var (
	errTooLarge  = errors.New("图片大小超出限制")
	errTooMany   = errors.New("图片尺寸超出限制")
	errDuplicate = errors.New("duplicate")
)

type Store struct {
	cfg *Config
	db  *sql.DB
	mu  sync.Mutex // 串行化写入：去重检查 + 落盘 + 入库

	setMu  sync.RWMutex // 保护 st / assets
	st     Settings
	assets wmAssets

	job regenJob
}

func (s *Store) originalPath(m *Image) string {
	return filepath.Join(s.cfg.Uploads, filepath.FromSlash(m.Pathname()))
}

// webpRel 是衍生图相对路径：原图 path/name 去掉扩展名 + .webp。
func webpRel(m *Image) string {
	stem := strings.TrimSuffix(m.Name, filepath.Ext(m.Name))
	if m.Path == "" {
		return stem + ".webp"
	}
	return m.Path + "/" + stem + ".webp"
}

func (s *Store) webpPath(m *Image) string {
	return filepath.Join(s.cfg.WebpDir, filepath.FromSlash(webpRel(m)))
}

const keyChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randomString(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(keyChars)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = keyChars[v.Int64()]
	}
	return string(b)
}

func hashes(data []byte) (string, string, string) {
	a := md5.Sum(data)
	b := sha1.Sum(data)
	c := sha256.Sum256(data)
	return hex.EncodeToString(a[:]), hex.EncodeToString(b[:]), hex.EncodeToString(c[:])
}

func (s *Store) bySHA256(sum string) (*Image, error) {
	m, err := scanImage(s.db.QueryRow("SELECT "+imageCols+" FROM images WHERE sha256=?", sum))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

func (s *Store) byKey(key string) (*Image, error) {
	m, err := scanImage(s.db.QueryRow("SELECT "+imageCols+" FROM images WHERE key=?", key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// Ingest 的输入。Path/Name 非空表示文件已在盘上（导入），否则新建。
type ingestReq struct {
	Data       []byte
	OriginName string
	CreatedAt  time.Time
	Key        string
	Path       string
	Name       string
	NoMark     bool // 衍生图不加可见水印
}

// ingest 校验、去重、落盘、生成衍生图并入库。去重命中时返回已有记录和 errDuplicate。
func (s *Store) ingest(r ingestReq) (*Image, error) {
	st := s.settings()
	if int64(len(r.Data)) > st.maxSize() {
		return nil, errTooLarge
	}
	f, err := sniff(r.Data)
	if err != nil {
		return nil, err
	}
	w, h := decodeConfig(r.Data)
	if w == 0 || h == 0 {
		return nil, errUnsupported
	}
	if w*h > st.MaxPixels {
		return nil, errTooMany
	}
	md5sum, sha1sum, sha256sum := hashes(r.Data)

	s.mu.Lock()
	defer s.mu.Unlock()

	if old, err := s.bySHA256(sha256sum); err != nil {
		return nil, err
	} else if old != nil {
		return old, errDuplicate
	}

	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	m := &Image{
		Key: r.Key, Path: r.Path, Name: r.Name, OriginName: r.OriginName,
		Size: int64(len(r.Data)), Mimetype: f.mime, Extension: f.ext,
		MD5: md5sum, SHA1: sha1sum, SHA256: sha256sum, Width: w, Height: h,
		CreatedAt: r.CreatedAt.Unix(), Watermark: !r.NoMark,
	}

	created := false
	if m.Name == "" {
		if err := s.writeNew(m, r.Data, r.CreatedAt); err != nil {
			return nil, err
		}
		created = true
	} else {
		m.Extension = strings.ToLower(strings.TrimPrefix(filepath.Ext(m.Name), "."))
		if m.Extension == "" {
			m.Extension = f.ext
		}
	}

	if err := s.writeWebP(m, r.Data, f); err != nil {
		fmt.Fprintf(os.Stderr, "webp %s: %v\n", m.Pathname(), err)
	}

	if err := s.insert(m); err != nil {
		if created {
			os.Remove(s.originalPath(m))
		}
		os.Remove(s.webpPath(m))
		return nil, err
	}
	return m, nil
}

// writeNew 按 日期目录 + uniqid 风格 13 位 hex 生成文件并以 O_EXCL 写入。
func (s *Store) writeNew(m *Image, data []byte, t time.Time) error {
	t = t.In(shanghai)
	m.Path = t.Format("2006/01/02")
	dir := filepath.Join(s.cfg.Uploads, filepath.FromSlash(m.Path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	sec, usec := t.Unix(), t.Nanosecond()/1000
	for i := 0; i < 1000; i++ {
		u := usec + i
		name := fmt.Sprintf("%08x%05x.%s", sec+int64(u/1_000_000), u%1_000_000, m.Extension)
		fh, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		m.Name = name
		if _, err := fh.Write(data); err != nil {
			fh.Close()
			os.Remove(fh.Name())
			return err
		}
		if err := fh.Close(); err != nil {
			os.Remove(fh.Name())
			return err
		}
		return nil
	}
	return errors.New("无法生成文件名")
}

var webpSem = make(chan struct{}, 2)

// webpOpts 按当前设置与该图的水印开关组装参数。
func (s *Store) webpOpts(m *Image) webpOpts {
	st, a := s.settings(), s.currentAssets()
	o := webpOpts{quality: st.WebpQuality, maxEdge: st.WebpMaxEdge}
	if st.Watermark.Enabled && m.Watermark {
		o.mk = &marker{st: st.Watermark, font: a.font, icon: a.icon}
	}
	if st.Meta.Enabled {
		meta := st.Meta
		o.meta = &meta
	}
	return o
}

func (s *Store) writeWebP(m *Image, data []byte, f format) error {
	webpSem <- struct{}{}
	out, err := makeWebP(data, f, s.webpOpts(m))
	<-webpSem
	if err != nil || out == nil {
		m.WebpSize = 0
		return err
	}
	p := s.webpPath(m)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	m.WebpSize = int64(len(out))
	return nil
}

func (s *Store) insert(m *Image) error {
	key := m.Key
	for i := 0; i < 20; i++ {
		if key == "" || i > 0 {
			key = randomString(6 + i/5)
		}
		res, err := s.db.Exec(`INSERT INTO images (key,path,name,origin_name,size,mimetype,extension,md5,sha1,sha256,width,height,webp_size,created_at,watermark)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			key, m.Path, m.Name, m.OriginName, m.Size, m.Mimetype, m.Extension, m.MD5, m.SHA1, m.SHA256,
			m.Width, m.Height, m.WebpSize, m.CreatedAt, m.Watermark)
		if err != nil {
			if strings.Contains(err.Error(), "images.key") {
				continue
			}
			return err
		}
		m.ID, _ = res.LastInsertId()
		m.Key = key
		return nil
	}
	return errors.New("无法生成 key")
}

func (s *Store) delete(key string) error {
	_, err := s.deleteMany([]string{key})
	return err
}

// errDB 包装数据库错误，regen 遇到它就中止（单张图的问题只跳过）。
type errDB struct{ error }

func (e errDB) Unwrap() error { return e.error }

// rebuild 按当前设置重新生成单张衍生图并更新 webp_size。
func (s *Store) rebuild(m *Image) error {
	data, err := os.ReadFile(s.originalPath(m))
	if err != nil {
		return err
	}
	f, err := sniff(data)
	if err != nil {
		return err
	}
	werr := s.writeWebP(m, data, f)
	if _, err := s.db.Exec("UPDATE images SET webp_size=? WHERE id=?", m.WebpSize, m.ID); err != nil {
		return errDB{err}
	}
	return werr
}

// setWatermark 切换单张的水印开关并立即重建它的衍生图。
func (s *Store) setWatermark(m *Image, on bool) error {
	if _, err := s.db.Exec("UPDATE images SET watermark=? WHERE id=?", on, m.ID); err != nil {
		return errDB{err}
	}
	m.Watermark = on
	return s.rebuild(m)
}

// regen 补生成（或 all=true 时重建）衍生图。progress 可为 nil，每处理一张回调一次。
func (s *Store) regen(all bool, progress func(done, total int)) (int, error) {
	rows, err := s.db.Query("SELECT " + imageCols + " FROM images ORDER BY id")
	if err != nil {
		return 0, err
	}
	var list []*Image
	for rows.Next() {
		m, err := scanImage(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, m)
	}
	rows.Close()
	n := 0
	for i, m := range list {
		if progress != nil {
			progress(i, len(list))
		}
		if !all && m.WebpSize > 0 {
			if _, err := os.Stat(s.webpPath(m)); err == nil {
				continue
			}
		}
		if err := s.rebuild(m); err != nil {
			fmt.Fprintf(os.Stderr, "webp %s: %v\n", m.Pathname(), err)
			if errors.As(err, new(errDB)) {
				return n, err
			}
			continue
		}
		n++
	}
	if progress != nil {
		progress(len(list), len(list))
	}
	return n, nil
}

// regenJob 是管理页触发的后台全量重建，同一时间只跑一个。
type regenJob struct {
	mu      sync.Mutex
	running bool
	done    int
	total   int
	ok      int
	err     string
	started int64
	ended   int64
}

type regenStatus struct {
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	OK      int    `json:"ok"`
	Error   string `json:"error"`
	Started int64  `json:"started"`
	Ended   int64  `json:"ended"`
}

func (s *Store) regenStatus() regenStatus {
	j := &s.job
	j.mu.Lock()
	defer j.mu.Unlock()
	return regenStatus{j.running, j.done, j.total, j.ok, j.err, j.started, j.ended}
}

// startRegen 启动后台重建；已在跑时返回 false。
func (s *Store) startRegen(all bool) bool {
	j := &s.job
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return false
	}
	j.running, j.done, j.total, j.ok, j.err = true, 0, 0, 0, ""
	j.started, j.ended = time.Now().Unix(), 0
	j.mu.Unlock()
	go func() {
		n, err := s.regen(all, func(done, total int) {
			j.mu.Lock()
			j.done, j.total = done, total
			j.mu.Unlock()
		})
		j.mu.Lock()
		j.running, j.ok, j.ended = false, n, time.Now().Unix()
		if err != nil {
			j.err = err.Error()
		}
		j.mu.Unlock()
	}()
	return true
}
