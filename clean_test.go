package main

import (
	"bytes"
	"image"
	"os"
	"regexp"
	"testing"

	"github.com/gen2brain/webp"
)

func TestCleanWebP(t *testing.T) {
	s := testStore(t)
	st := s.settings()
	st.WebpMaxEdge = 2560
	st.Watermark.MinEdge = 100
	if err := s.saveSettings(st); err != nil {
		t.Fatal(err)
	}
	m, err := s.ingest(ingestReq{Data: pngBytes(600, 400, 80), OriginName: "a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.makeClean(m); err != nil {
		t.Fatal(err)
	}
	stem := m.Name[:len(m.Name)-4]
	if !regexp.MustCompile(`^` + stem + `-[A-Za-z0-9]{10}\.webp$`).MatchString(m.CleanName) {
		t.Fatalf("文件名应带随机串: %s", m.CleanName)
	}
	read := func(p string) (*image.NRGBA, []byte) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		img, err := webp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return toNRGBA(img), b
	}
	marked, _ := read(s.webpPath(m))
	clean, raw := read(s.cleanPath(m))
	if !changed(marked, clean, image.Rect(400, 330, 600, 400)) {
		t.Fatal("无水印版右下角应与带水印版不同")
	}
	if !bytes.Contains(raw, []byte("EXIF")) {
		t.Fatal("无水印版应保留 Meta")
	}

	// 重建：文件名不变，记录同步
	name := m.CleanName
	if err := s.makeClean(m); err != nil || m.CleanName != name {
		t.Fatalf("重建后链接应不变: %v %s", err, m.CleanName)
	}
	got, _ := s.byKey(m.Key)
	if got.CleanName != name || got.CleanSize == 0 {
		t.Fatalf("记录: %+v", got)
	}
	if _, err := s.regen(true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.cleanPath(got)); err != nil {
		t.Fatal("全量重建后无水印版应仍在")
	}

	// 删除无水印版
	if err := s.removeClean(got); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.cleanPath(&Image{Path: m.Path, CleanName: name})); !os.IsNotExist(err) {
		t.Fatal("文件应已删除")
	}
	if again, _ := s.byKey(m.Key); again.CleanName != "" {
		t.Fatal("记录应已清空")
	}

	// 删图时一并删除
	if err := s.makeClean(got); err != nil {
		t.Fatal(err)
	}
	p := s.cleanPath(got)
	if err := s.delete(got.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("删图后无水印版应一并删除")
	}
}

func TestCleanGIFRejected(t *testing.T) {
	s := testStore(t)
	gif := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x00\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	m, err := s.ingest(ingestReq{Data: gif, OriginName: "a.gif"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.makeClean(m); err != errNoClean || m.CleanName != "" {
		t.Fatalf("GIF 应拒绝: %v %q", err, m.CleanName)
	}
}
