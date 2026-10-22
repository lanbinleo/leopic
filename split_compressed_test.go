package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func cr(key, path, name, display string, w, h int) lskyRow {
	return lskyRow{key: key, path: path, name: name, display: display, w: w, h: h, created: time.Now()}
}

func TestSplitCompressed(t *testing.T) {
	list := []lskyRow{
		// 常规配对：png 原图 + jpg 压缩图
		cr("a1", "2026/06/16", "6a30a79c531a4.png", "photo.png", 1024, 1024),
		cr("a2", "2026/06/16", "6a30a79ce89e6.jpg", "photo-compressed.jpg", 1024, 1024),
		// 同名多候选（image.png）：按上传时间消歧
		cr("b1", "2026/08/01", "6a6d7db49420f.png", "image.png", 800, 600),
		cr("b2", "2026/08/01", "6a6d7db4ee372.jpg", "image-compressed.jpg", 800, 600),
		cr("b3", "2026/08/02", "6a6d8db49420f.png", "image.png", 800, 600), // 隔天的同名图，不该被配对
		// 无原图的压缩图：保留（唯一副本）
		cr("c1", "2026/08/11", "6a7ace429a0f5.webp", "ep00-cover-watermarked-compressed.webp", 1360, 768),
		// 同名但不同目录/不同尺寸：不剔除
		cr("d1", "2026/05/01", "6a30a11111111.png", "same.png", 100, 100),
		cr("d2", "2026/06/16", "6a30a79c99999.jpg", "same-compressed.jpg", 1024, 1024),
	}

	kept, pairs := splitCompressed(list)
	if len(pairs) != 2 {
		t.Fatalf("应剔除 2 对, got %d: %+v", len(pairs), pairs)
	}
	if pairs[0].orig.name != "6a30a79c531a4.png" || pairs[1].orig.name != "6a6d7db49420f.png" {
		t.Fatalf("配对原图错误: %+v", pairs)
	}
	if len(kept) != 6 {
		t.Fatalf("应保留 6 项（8 - 2 对压缩图）, got %d: %+v", len(kept), kept)
	}
	keptNames := map[string]bool{}
	for _, r := range kept {
		keptNames[r.name] = true
	}
	for _, must := range []string{"6a30a79c531a4.png", "6a6d7db49420f.png", "6a6d8db49420f.png", "6a7ace429a0f5.webp", "6a30a11111111.png", "6a30a79c99999.jpg"} {
		if !keptNames[must] {
			t.Fatalf("应保留 %s", must)
		}
	}
}

func TestWriteRedirectMap(t *testing.T) {
	cfg := &Config{ImgPrefix: "/img", WebpPrefix: "/webp"}
	pairs := []compressedPair{
		{comp: cr("a2", "2026/06/16", "6a30a79ce89e6.jpg", "photo-compressed.jpg", 1024, 1024),
			orig: cr("a1", "2026/06/16", "6a30a79c531a4.png", "photo.png", 1024, 1024)},
		{comp: cr("g2", "2026/01/01", "aaa0000000001.jpg", "loop-compressed.jpg", 100, 100),
			orig: cr("g1", "2026/01/01", "aaa0000000002.gif", "loop.gif", 100, 100)}, // GIF 回落原图
	}
	dir := t.TempDir()
	path := dir + "/map.conf"
	if err := writeRedirectMap(cfg, pairs, path); err != nil {
		t.Fatal(err)
	}
	data, err := readFileStr(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"map $uri $leopic_redirect {",
		"/img/2026/06/16/6a30a79ce89e6.jpg  /webp/2026/06/16/6a30a79c531a4.webp;",
		"/img/2026/01/01/aaa0000000001.jpg  /img/2026/01/01/aaa0000000002.gif;",
	} {
		if !strings.Contains(data, want) {
			t.Fatalf("map 缺少: %s\n%s", want, data)
		}
	}
}

func readFileStr(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
