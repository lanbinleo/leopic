package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 只放启动前必须确定、改错会锁死后台或断链的项。
// 图片质量、上传上限、水印等可调参数在管理页设置（见 settings.go）。
type Config struct {
	Addr          string
	Uploads       string
	WebpDir       string
	Assets        string
	DB            string
	BaseURL       string
	ImgPrefix     string
	WebpPrefix    string
	Password      string
	AdminPath     string
	AdminRedirect string
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", key)
	}
	return n, nil
}

func prefix(v string) string {
	return "/" + strings.Trim(v, "/")
}

func loadConfig() (*Config, error) {
	data := env("LEOPIC_DATA", "/data")
	c := &Config{
		Addr:       env("LEOPIC_ADDR", ":8080"),
		Uploads:    env("LEOPIC_UPLOADS", filepath.Join(data, "uploads")),
		WebpDir:    env("LEOPIC_WEBP_DIR", filepath.Join(data, "webp")),
		Assets:     env("LEOPIC_ASSETS_DIR", filepath.Join(data, "assets")),
		DB:         env("LEOPIC_DB", filepath.Join(data, "leopic.db")),
		BaseURL:    strings.TrimRight(env("LEOPIC_BASE_URL", ""), "/"),
		ImgPrefix:  prefix(env("LEOPIC_IMG_PREFIX", "/img")),
		WebpPrefix: prefix(env("LEOPIC_WEBP_PREFIX", "/webp")),
		Password:   os.Getenv("LEOPIC_PASSWORD"),
	}
	if ap := env("LEOPIC_ADMIN_PATH", ""); ap != "" {
		c.AdminPath = prefix(ap)
	}
	c.AdminRedirect = env("LEOPIC_ADMIN_REDIRECT", "")
	return c, nil
}

func (c *Config) validateServe() error {
	if c.BaseURL == "" {
		return errors.New("缺少 LEOPIC_BASE_URL")
	}
	if c.Password == "" {
		return errors.New("缺少 LEOPIC_PASSWORD")
	}
	if c.AdminPath != "" {
		if c.AdminPath == "/" {
			return errors.New("LEOPIC_ADMIN_PATH 不能是 /")
		}
		if c.AdminRedirect == "" {
			return errors.New("设置 LEOPIC_ADMIN_PATH 时必须同时设置 LEOPIC_ADMIN_REDIRECT")
		}
	}
	return nil
}
