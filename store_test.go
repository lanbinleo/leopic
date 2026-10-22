package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"regexp"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{
		Uploads: filepath.Join(dir, "uploads"), WebpDir: filepath.Join(dir, "webp"),
		MaxSize: 10 << 20, MaxPixels: 50_000_000, WebpQuality: 82, WebpMaxEdge: 64,
	}
	db, err := openDB(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{cfg: cfg, db: db}
}

func pngBytes(w, h int, c uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = c
	}
	img.Set(0, 0, color.NRGBA{1, 2, 3, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestIngestNamingAndDedupe(t *testing.T) {
	s := testStore(t)
	m, err := s.ingest(ingestReq{Data: pngBytes(200, 100, 9), OriginName: "a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}/\d{2}/\d{2}$`).MatchString(m.Path) ||
		!regexp.MustCompile(`^[0-9a-f]{13}\.png$`).MatchString(m.Name) {
		t.Fatalf("命名不符: %s/%s", m.Path, m.Name)
	}
	if len(m.Key) != 6 || m.WebpSize == 0 || m.Width != 200 {
		t.Fatalf("记录异常: %+v", m)
	}
	dup, err := s.ingest(ingestReq{Data: pngBytes(200, 100, 9), OriginName: "b.png"})
	if !errors.Is(err, errDuplicate) || dup.Key != m.Key {
		t.Fatalf("应命中去重: %v %+v", err, dup)
	}
	if _, err := s.ingest(ingestReq{Data: []byte("not an image")}); err != errUnsupported {
		t.Fatalf("应拒绝非图片: %v", err)
	}
	if err := s.delete(m.Key); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.byKey(m.Key); got != nil {
		t.Fatal("删除后仍存在")
	}
}

func TestSanctumToken(t *testing.T) {
	s := testStore(t)
	tok, err := createToken(s.db, "x")
	if err != nil {
		t.Fatal(err)
	}
	if !checkToken(s.db, tok) {
		t.Fatal("id|plain 应通过")
	}
	if !checkToken(s.db, tok[regexp.MustCompile(`^\d+\|`).FindStringIndex(tok)[1]:]) {
		t.Fatal("纯 plain 应通过")
	}
	if checkToken(s.db, "1|wrong") || checkToken(s.db, "") {
		t.Fatal("错误 token 不应通过")
	}
}

func TestOrientation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	out := applyOrientation(src, 6) // 顺时针 90°
	if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
		t.Fatalf("尺寸 %v", b)
	}
	if r, _, _, _ := out.At(1, 0).RGBA(); r>>8 != 255 {
		t.Fatal("左上像素应转到右上")
	}
}

func TestDeleteManyAndNames(t *testing.T) {
	s := testStore(t)
	a, _ := s.ingest(ingestReq{Data: pngBytes(20, 20, 1), OriginName: "图.png"})
	b, _ := s.ingest(ingestReq{Data: pngBytes(20, 20, 2), OriginName: "图.png"})
	c, _ := s.ingest(ingestReq{Data: pngBytes(20, 20, 3), OriginName: ""})
	list, err := s.byKeys([]string{b.Key, "nope", a.Key, b.Key})
	if err != nil || len(list) != 2 || list[0].Key != b.Key {
		t.Fatalf("byKeys: %v %d", err, len(list))
	}
	u := uniqueNamer{}
	if n1, n2 := u.take(downloadName(a, false)), u.take(downloadName(b, false)); n1 != "图.png" || n2 != "图 (2).png" {
		t.Fatalf("重名处理: %s %s", n1, n2)
	}
	if n := downloadName(a, true); n != "图.webp" {
		t.Fatalf("webp 名: %s", n)
	}
	if n := downloadName(c, false); n != c.Name {
		t.Fatalf("空原名应回落: %s", n)
	}
	if n := downloadName(&Image{OriginName: `a/b:c?.png`, Extension: "png"}, false); n != "b_c_.png" {
		t.Fatalf("非法字符: %s", n)
	}
	n, err := s.deleteMany([]string{a.Key, b.Key, "nope"})
	if err != nil || n != 2 {
		t.Fatalf("deleteMany: %v %d", err, n)
	}
	if left, _ := s.byKeys([]string{a.Key, b.Key, c.Key}); len(left) != 1 {
		t.Fatalf("剩余 %d", len(left))
	}
}
