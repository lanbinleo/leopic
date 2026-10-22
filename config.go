package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr          string
	Uploads       string
	WebpDir       string
	DB            string
	BaseURL       string
	ImgPrefix     string
	WebpPrefix    string
	Password      string
	Email         string
	AdminPath     string
	AdminRedirect string
	MaxSize       int64
	MaxPixels     int
	WebpQuality   int
	WebpMaxEdge   int
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
		DB:         env("LEOPIC_DB", filepath.Join(data, "leopic.db")),
		BaseURL:    strings.TrimRight(env("LEOPIC_BASE_URL", ""), "/"),
		ImgPrefix:  prefix(env("LEOPIC_IMG_PREFIX", "/img")),
		WebpPrefix: prefix(env("LEOPIC_WEBP_PREFIX", "/webp")),
		Password:   os.Getenv("LEOPIC_PASSWORD"),
		Email:      env("LEOPIC_EMAIL", ""),
	}
	if ap := env("LEOPIC_ADMIN_PATH", ""); ap != "" {
		c.AdminPath = prefix(ap)
	}
	c.AdminRedirect = env("LEOPIC_ADMIN_REDIRECT", "")
	maxMB, err := envInt("LEOPIC_MAX_SIZE_MB", 50)
	if err != nil {
		return nil, err
	}
	c.MaxSize = int64(maxMB) << 20
	if c.MaxPixels, err = envInt("LEOPIC_MAX_PIXELS", 50_000_000); err != nil {
		return nil, err
	}
	if c.WebpQuality, err = envInt("LEOPIC_WEBP_QUALITY", 82); err != nil {
		return nil, err
	}
	if c.WebpMaxEdge, err = envInt("LEOPIC_WEBP_MAX_EDGE", 2560); err != nil {
		return nil, err
	}
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
