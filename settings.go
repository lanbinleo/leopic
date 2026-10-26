package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Settings 是管理页可调的参数，整体以 JSON 存在 settings 表的 "app" 键下。
type Settings struct {
	WebpQuality int               `json:"webp_quality"`
	WebpMaxEdge int               `json:"webp_max_edge"`
	MaxSizeMB   int               `json:"max_size_mb"`
	MaxPixels   int               `json:"max_pixels"`
	Email       string            `json:"email"`
	Watermark   WatermarkSettings `json:"watermark"`
	Meta        MetaSettings      `json:"meta"`
}

// WatermarkSettings 可见水印，只加在 WebP 衍生图上。尺寸类参数都是短边的百分比。
type WatermarkSettings struct {
	Enabled  bool    `json:"enabled"`
	Mode     string  `json:"mode"` // text | icon | both
	Text     string  `json:"text"`
	Opacity  float64 `json:"opacity"`
	Size     float64 `json:"size"`
	Margin   float64 `json:"margin"`
	Position string  `json:"position"` // br | bl | tr | tl
	MinEdge  int     `json:"min_edge"` // 短边小于此值不加
}

// MetaSettings 写入 WebP 的 EXIF / XMP。
type MetaSettings struct {
	Enabled   bool   `json:"enabled"`
	Artist    string `json:"artist"`
	Copyright string `json:"copyright"`
}

func defaultSettings() Settings {
	return Settings{
		WebpQuality: 82,
		WebpMaxEdge: 2560,
		MaxSizeMB:   50,
		MaxPixels:   50_000_000,
		Watermark: WatermarkSettings{
			Enabled: true, Mode: "text", Text: "@子夜集 leoreo.me",
			Opacity: 0.35, Size: 2.4, Margin: 2, Position: "br", MinEdge: 400,
		},
		Meta: MetaSettings{Enabled: true, Artist: "子夜集", Copyright: "© 子夜集 leoreo.me"},
	}
}

// legacyEnv 是旧版的环境变量，仅在库里还没有设置时导入一次。
var legacyEnv = []string{"LEOPIC_MAX_SIZE_MB", "LEOPIC_MAX_PIXELS", "LEOPIC_WEBP_QUALITY", "LEOPIC_WEBP_MAX_EDGE", "LEOPIC_EMAIL"}

func settingsFromEnv() (Settings, error) {
	st := defaultSettings()
	var err error
	if st.MaxSizeMB, err = envInt("LEOPIC_MAX_SIZE_MB", st.MaxSizeMB); err != nil {
		return st, err
	}
	if st.MaxPixels, err = envInt("LEOPIC_MAX_PIXELS", st.MaxPixels); err != nil {
		return st, err
	}
	if st.WebpQuality, err = envInt("LEOPIC_WEBP_QUALITY", st.WebpQuality); err != nil {
		return st, err
	}
	if st.WebpMaxEdge, err = envInt("LEOPIC_WEBP_MAX_EDGE", st.WebpMaxEdge); err != nil {
		return st, err
	}
	st.Email = env("LEOPIC_EMAIL", "")
	return st, nil
}

// loadSettings 读库；库里没有时从旧环境变量导入并落库。
func (s *Store) loadSettings() error {
	var raw string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key='app'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		st, err := settingsFromEnv()
		if err != nil {
			return err
		}
		return s.saveSettings(st)
	}
	if err != nil {
		return err
	}
	st := defaultSettings() // 新增字段取默认值
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return fmt.Errorf("settings 损坏: %w", err)
	}
	if err := st.validate(); err != nil {
		return fmt.Errorf("settings 不合法: %w", err)
	}
	for _, k := range legacyEnv {
		if os.Getenv(k) != "" {
			fmt.Fprintf(os.Stderr, "提示: %s 已改为在管理页设置，环境变量不再生效\n", k)
		}
	}
	s.setMu.Lock()
	s.st = st
	s.setMu.Unlock()
	return nil
}

func (s *Store) saveSettings(st Settings) error {
	if err := st.validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	if _, err := s.db.Exec("INSERT INTO settings (key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(b)); err != nil {
		return err
	}
	s.setMu.Lock()
	s.st = st
	s.setMu.Unlock()
	return nil
}

func (s *Store) settings() Settings {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	return s.st
}

func (st Settings) maxSize() int64 { return int64(st.MaxSizeMB) << 20 }

func between[T int | float64](v, lo, hi T) bool { return v >= lo && v <= hi }

// validate 校验并就地规整（去空白）。
func (st *Settings) validate() error {
	st.Email = strings.TrimSpace(st.Email)
	w := &st.Watermark
	w.Text = strings.TrimSpace(w.Text)
	st.Meta.Artist = strings.TrimSpace(st.Meta.Artist)
	st.Meta.Copyright = strings.TrimSpace(st.Meta.Copyright)
	switch {
	case !between(st.WebpQuality, 1, 100):
		return errors.New("WebP 质量需在 1–100")
	case !between(st.WebpMaxEdge, 64, 16384):
		return errors.New("最长边需在 64–16384")
	case !between(st.MaxSizeMB, 1, 1024):
		return errors.New("单文件上限需在 1–1024 MB")
	case !between(st.MaxPixels, 1_000_000, 500_000_000):
		return errors.New("像素上限需在 100 万–5 亿")
	case len(st.Email) > 200:
		return errors.New("邮箱过长")
	case w.Mode != "text" && w.Mode != "icon" && w.Mode != "both":
		return errors.New("水印类型不合法")
	case w.Position != "br" && w.Position != "bl" && w.Position != "tr" && w.Position != "tl":
		return errors.New("水印位置不合法")
	case len([]rune(w.Text)) > 100:
		return errors.New("水印文字最多 100 字")
	case !between(w.Opacity, 0.05, 1):
		return errors.New("不透明度需在 0.05–1")
	case !between(w.Size, 0.5, 20):
		return errors.New("水印大小需在 0.5%–20%")
	case !between(w.Margin, 0, 20):
		return errors.New("边距需在 0%–20%")
	case !between(w.MinEdge, 0, 10000):
		return errors.New("最小短边需在 0–10000")
	case len([]rune(st.Meta.Artist)) > 200 || len([]rune(st.Meta.Copyright)) > 200:
		return errors.New("作者 / 版权最多 200 字")
	}
	return nil
}
