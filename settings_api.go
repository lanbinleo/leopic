package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"strings"
)

func (s *Server) settingsView() map[string]any {
	a := s.store.currentAssets()
	return map[string]any{
		"settings":    s.store.settings(),
		"defaults":    defaultSettings(),
		"custom_font": a.customFont,
		"has_icon":    a.icon != nil,
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ok(w, "success", s.settingsView())
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var st Settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&st); err != nil {
		fail(w, 200, "参数错误")
		return
	}
	if err := s.store.saveSettings(st); err != nil {
		fail(w, 200, err.Error())
		return
	}
	ok(w, "已保存", s.settingsView())
}

// previewSample 是没有指定图片时用的渐变样图，左上深右下浅，便于看清水印在两种底色上的效果。
func previewSample() *image.NRGBA {
	const w, h = 1200, 800
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.SetNRGBA(x, y, color.NRGBA{uint8(40 + 200*t), uint8(70 + 160*t), uint8(110 + 120*t), 255})
		}
	}
	return img
}

// handlePreview 用请求里（未保存）的设置渲染一张预览 WebP。可带 key 用某张已有图片。
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings Settings `json:"settings"`
		Key      string   `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		fail(w, 200, "参数错误")
		return
	}
	st := body.Settings
	if err := st.validate(); err != nil {
		fail(w, 200, err.Error())
		return
	}
	var data []byte
	var f format
	if body.Key != "" {
		m, err := s.store.byKey(body.Key)
		if err != nil || m == nil {
			fail(w, 200, "图片不存在")
			return
		}
		if data, err = os.ReadFile(s.store.originalPath(m)); err != nil {
			fail(w, 200, "文件不存在")
			return
		}
		if f, err = sniff(data); err != nil || f.ext == "gif" {
			fail(w, 200, "该图片不能预览（GIF 不生成衍生图）")
			return
		}
	}
	a := s.store.currentAssets()
	wm := st.Watermark
	wm.Enabled = true // 预览总是画出来，便于对比
	o := webpOpts{quality: st.WebpQuality, maxEdge: min(st.WebpMaxEdge, 1600),
		mk: &marker{st: wm, font: a.font, icon: a.icon}}
	if !st.Watermark.Enabled {
		o.mk = nil
	}

	webpSem <- struct{}{}
	var out []byte
	var err error
	if data != nil {
		out, err = makeWebP(data, f, o)
	} else {
		out, err = encodeNRGBA(previewSample(), o)
	}
	<-webpSem
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: %v\n", err)
		fail(w, 200, "预览失败")
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(out)
}

var assetLimits = map[string]int64{"font": 30 << 20, "icon": 5 << 20}

func (s *Server) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	limit, known := assetLimits[name]
	if !known {
		fail(w, 404, "未知资源")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(w, 200, "文件过大或格式错误")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, 200, "请选择文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		fail(w, 200, "文件过大")
		return
	}
	if name == "font" {
		err = s.store.saveFont(data)
	} else {
		err = s.store.saveIcon(data)
	}
	if err != nil {
		fail(w, 200, err.Error())
		return
	}
	ok(w, "已上传", s.settingsView())
}

func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	file := map[string]string{"font": fontFile, "icon": iconFile}[r.PathValue("name")]
	if file == "" {
		fail(w, 404, "未知资源")
		return
	}
	if err := s.store.removeAsset(file); err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	ok(w, "已删除", s.settingsView())
}

func (s *Server) handleGetIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, s.store.assetPath(iconFile))
}

func (s *Server) handleStartRegen(w http.ResponseWriter, r *http.Request) {
	all := readParams(r).Get("all") != "false"
	if !s.store.startRegen(all) {
		fail(w, 200, "已有重建任务在运行")
		return
	}
	ok(w, "已开始", s.store.regenStatus())
}

func (s *Server) handleRegenStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, "success", s.store.regenStatus())
}

func (s *Server) handleSetWatermark(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.byKey(r.PathValue("key"))
	if err != nil || m == nil {
		fail(w, 200, "图片不存在")
		return
	}
	on := strings.TrimSpace(readParams(r).Get("watermark")) != "false"
	if err := s.store.setWatermark(m, on); err != nil {
		fmt.Fprintf(os.Stderr, "watermark %s: %v\n", m.Key, err)
		fail(w, 200, "重建衍生图失败")
		return
	}
	ok(w, "已更新", map[string]any{"watermark": m.Watermark, "webp_size": kb(m.WebpSize), "links": s.links(m)})
}

// cleanItem 是无水印版操作后返回给前端的单张状态。
type cleanItem struct {
	Key       string  `json:"key"`
	CleanSize float64 `json:"clean_size"`
	Links     links   `json:"links"`
	Error     string  `json:"error,omitempty"`
}

func readKeys(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.Keys) == 0 {
		fail(w, 200, "请选择图片")
		return nil, false
	}
	if len(body.Keys) > maxBatch {
		fail(w, 200, fmt.Sprintf("一次最多 %d 张", maxBatch))
		return nil, false
	}
	return body.Keys, true
}

// handleMakeClean：JSON {keys}，逐张生成无水印版（已有则按当前设置重建），单张失败不影响其余。
func (s *Server) handleMakeClean(w http.ResponseWriter, r *http.Request) {
	keys, good := readKeys(w, r)
	if !good {
		return
	}
	list, err := s.store.byKeys(keys)
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	items := []cleanItem{}
	failed := 0
	for _, m := range list {
		it := cleanItem{Key: m.Key}
		if err := s.store.makeClean(m); err != nil {
			failed++
			it.Error = "生成失败"
			if err == errNoClean {
				it.Error = err.Error()
			} else {
				fmt.Fprintf(os.Stderr, "clean %s: %v\n", m.Key, err)
			}
		}
		it.CleanSize, it.Links = kb(m.CleanSize), s.links(m)
		items = append(items, it)
	}
	ok(w, "success", map[string]any{"items": items, "failed": failed})
}

func (s *Server) handleDeleteClean(w http.ResponseWriter, r *http.Request) {
	keys, good := readKeys(w, r)
	if !good {
		return
	}
	list, err := s.store.byKeys(keys)
	if err != nil {
		fail(w, 200, "服务异常，请稍后再试")
		return
	}
	items := []cleanItem{}
	for _, m := range list {
		if err := s.store.removeClean(m); err != nil {
			fail(w, 200, "服务异常，请稍后再试")
			return
		}
		items = append(items, cleanItem{Key: m.Key, Links: s.links(m)})
	}
	ok(w, "success", map[string]any{"items": items})
}
