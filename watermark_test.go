package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/gen2brain/webp"
)

func flat(w, h int, c uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = c
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func changed(a, b *image.NRGBA, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if a.NRGBAAt(x, y) != b.NRGBAAt(x, y) {
				return true
			}
		}
	}
	return false
}

func TestMarkerCorner(t *testing.T) {
	st := defaultSettings().Watermark
	img := flat(1200, 800, 60)
	orig := flat(1200, 800, 60)
	mk := &marker{st: st, font: defaultFont}
	if err := mk.apply(img); err != nil {
		t.Fatal(err)
	}
	if !changed(img, orig, image.Rect(600, 600, 1200, 800)) {
		t.Fatal("右下角应有水印")
	}
	if changed(img, orig, image.Rect(0, 0, 600, 400)) {
		t.Fatal("左上角不应被改动")
	}

	st.Position = "tl"
	img = flat(1200, 800, 60)
	(&marker{st: st, font: defaultFont}).apply(img)
	if !changed(img, orig, image.Rect(0, 0, 600, 200)) || changed(img, orig, image.Rect(600, 400, 1200, 800)) {
		t.Fatal("tl 位置不对")
	}

	small := flat(300, 300, 60)
	(&marker{st: defaultSettings().Watermark, font: defaultFont}).apply(small)
	if changed(small, flat(300, 300, 60), small.Bounds()) {
		t.Fatal("短边小于 MinEdge 不应加水印")
	}
}

func TestMarkerIcon(t *testing.T) {
	st := defaultSettings().Watermark
	st.Mode = "icon"
	icon := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for i := range icon.Pix {
		icon.Pix[i] = 255
	}
	img := flat(1000, 1000, 0)
	(&marker{st: st, font: defaultFont, icon: icon}).apply(img)
	if !changed(img, flat(1000, 1000, 0), image.Rect(800, 800, 1000, 1000)) {
		t.Fatal("应画出图标")
	}
	// icon 模式但没有图标：什么都不画
	img = flat(1000, 1000, 0)
	(&marker{st: st, font: defaultFont}).apply(img)
	if changed(img, flat(1000, 1000, 0), img.Bounds()) {
		t.Fatal("无图标时不应改动")
	}
}

func TestWebPMeta(t *testing.T) {
	meta := defaultSettings().Meta
	for _, alpha := range []bool{false, true} {
		img := flat(50, 30, 120)
		if alpha {
			img.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0})
		}
		out, err := encodeNRGBA(img, webpOpts{quality: 80, meta: &meta})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte("EXIF")) || !bytes.Contains(out, []byte("XMP ")) ||
			!bytes.Contains(out, []byte(meta.Copyright)) {
			t.Fatal("缺少元数据块")
		}
		if n := int(out[4]) | int(out[5])<<8 | int(out[6])<<16 | int(out[7])<<24; n != len(out)-8 {
			t.Fatalf("RIFF 长度 %d != %d", n, len(out)-8)
		}
		dec, err := webp.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("加元数据后应能解码: %v", err)
		}
		if b := dec.Bounds(); b.Dx() != 50 || b.Dy() != 30 {
			t.Fatalf("尺寸 %v", b)
		}
	}
}

func TestSetWatermarkRebuilds(t *testing.T) {
	s := testStore(t)
	st := s.settings()
	st.WebpMaxEdge = 2560
	st.Watermark.MinEdge = 100
	if err := s.saveSettings(st); err != nil {
		t.Fatal(err)
	}
	marked, err := s.ingest(ingestReq{Data: pngBytes(600, 400, 80), OriginName: "a.png"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.ingest(ingestReq{Data: pngBytes(600, 400, 81), OriginName: "b.png", NoMark: true})
	if err != nil || plain.Watermark {
		t.Fatalf("NoMark 应记录为不加水印: %v %+v", err, plain)
	}
	decode := func(m *Image) *image.NRGBA {
		b, err := os.ReadFile(s.webpPath(m))
		if err != nil {
			t.Fatal(err)
		}
		img, err := webp.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return toNRGBA(img)
	}
	corner := image.Rect(400, 330, 600, 400)
	if !changed(decode(marked), decode(plain), corner) {
		t.Fatal("两张底色几乎相同，加水印的右下角应明显不同")
	}
	if err := s.setWatermark(marked, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.byKey(marked.Key)
	if got.Watermark {
		t.Fatal("开关应已落库")
	}
	a, b := decode(marked), decode(plain)
	diff := 0
	for y := corner.Min.Y; y < corner.Max.Y; y++ {
		for x := corner.Min.X; x < corner.Max.X; x++ {
			if d := int(a.NRGBAAt(x, y).R) - int(b.NRGBAAt(x, y).R); d > 8 || d < -8 {
				diff++
			}
		}
	}
	if diff > 0 {
		t.Fatalf("关闭水印后右下角应与无水印图一致，差异像素 %d", diff)
	}
}

func TestSettingsValidate(t *testing.T) {
	st := defaultSettings()
	if err := st.validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*Settings){
		func(s *Settings) { s.WebpQuality = 0 },
		func(s *Settings) { s.Watermark.Position = "center" },
		func(s *Settings) { s.Watermark.Opacity = 2 },
		func(s *Settings) { s.Watermark.Mode = "x" },
	}
	for i, f := range bad {
		st := defaultSettings()
		f(&st)
		if st.validate() == nil {
			t.Fatalf("case %d 应校验失败", i)
		}
	}
}

func TestSettingsPersistAndEnvImport(t *testing.T) {
	t.Setenv("LEOPIC_WEBP_QUALITY", "70")
	dir := t.TempDir()
	db, err := openDB(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{cfg: &Config{}, db: db}
	if err := s.loadSettings(); err != nil {
		t.Fatal(err)
	}
	if s.settings().WebpQuality != 70 {
		t.Fatal("首次启动应导入旧环境变量")
	}
	st := s.settings()
	st.WebpQuality = 90
	s.saveSettings(st)
	t.Setenv("LEOPIC_WEBP_QUALITY", "60")
	s2 := &Store{cfg: &Config{}, db: db}
	if err := s2.loadSettings(); err != nil || s2.settings().WebpQuality != 90 {
		t.Fatalf("之后应以库为准: %v %d", err, s2.settings().WebpQuality)
	}
}
