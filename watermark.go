package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// 内嵌 Noto Sans SC Medium 的 GB2312 + ASCII + 常用标点子集（OFL，见 fonts/OFL.txt）。
//
//go:embed fonts/NotoSansSC-Medium-subset.ttf
var embeddedFont []byte

var defaultFont = mustFont(embeddedFont)

func mustFont(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic(err)
	}
	return f
}

// 用户上传的覆盖资源，放在 {ASSETS}/ 下。
const (
	fontFile = "font"
	iconFile = "icon.png"
)

type wmAssets struct {
	font       *opentype.Font // nil = 用内嵌字体
	icon       image.Image
	customFont bool
}

func (s *Store) assetPath(name string) string { return filepath.Join(s.cfg.Assets, name) }

// loadAssets 读取覆盖字体与图标；损坏的文件忽略并提示。
func (s *Store) loadAssets() {
	a := wmAssets{font: defaultFont}
	if b, err := os.ReadFile(s.assetPath(fontFile)); err == nil {
		if f, err := opentype.Parse(b); err == nil {
			a.font, a.customFont = f, true
		} else {
			os.Stderr.WriteString("自定义字体无法解析，回落内嵌字体\n")
		}
	}
	if b, err := os.ReadFile(s.assetPath(iconFile)); err == nil {
		if img, err := png.Decode(bytes.NewReader(b)); err == nil {
			a.icon = img
		}
	}
	s.setMu.Lock()
	s.assets = a
	s.setMu.Unlock()
}

func (s *Store) currentAssets() wmAssets {
	s.setMu.RLock()
	defer s.setMu.RUnlock()
	return s.assets
}

var errBadAsset = errors.New("文件无法解析")

// saveFont 校验后保存覆盖字体（TTF / OTF）。
func (s *Store) saveFont(b []byte) error {
	if _, err := opentype.Parse(b); err != nil {
		return errBadAsset
	}
	return s.writeAsset(fontFile, b)
}

// saveIcon 解码任意支持的格式，统一转存为 PNG。
func (s *Store) saveIcon(b []byte) error {
	f, err := sniff(b)
	if err != nil {
		return errBadAsset
	}
	img, err := decodeImage(b, f)
	if err != nil {
		return errBadAsset
	}
	if r := img.Bounds(); r.Dx() > 2048 || r.Dy() > 2048 {
		return errors.New("图标最大 2048×2048")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return s.writeAsset(iconFile, buf.Bytes())
}

func (s *Store) writeAsset(name string, b []byte) error {
	if err := os.MkdirAll(s.cfg.Assets, 0o755); err != nil {
		return err
	}
	p := s.assetPath(name)
	if err := os.WriteFile(p+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		os.Remove(p + ".tmp")
		return err
	}
	s.loadAssets()
	return nil
}

func (s *Store) removeAsset(name string) error {
	if err := os.Remove(s.assetPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.loadAssets()
	return nil
}

// ---------- 可见水印 ----------

type marker struct {
	st   WatermarkSettings
	font *opentype.Font
	icon image.Image
}

// apply 在 dst 上画水印。短边不足 MinEdge、或没有可画的内容时不动。
func (mk *marker) apply(dst *image.NRGBA) error {
	b := dst.Bounds()
	short := min(b.Dx(), b.Dy())
	if short < mk.st.MinEdge || short < 32 {
		return nil
	}
	text := mk.st.Text
	if mk.st.Mode == "icon" {
		text = ""
	}
	icon := mk.icon
	if mk.st.Mode == "text" {
		icon = nil
	}
	if text == "" && icon == nil {
		return nil
	}

	px := max(float64(short)*mk.st.Size/100, 8)
	margin := int(float64(short) * mk.st.Margin / 100)
	alpha := uint8(mk.st.Opacity*255 + 0.5)

	// 文字先画进一张 Alpha 蒙版，再按不透明度叠白字 + 淡阴影。
	var mask *image.Alpha
	var textW, textH, ascent int
	if text != "" {
		face, err := opentype.NewFace(mk.font, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return err
		}
		defer face.Close()
		met := face.Metrics()
		ascent = met.Ascent.Ceil()
		textH = ascent + met.Descent.Ceil()
		textW = font.MeasureString(face, text).Ceil()
		mask = image.NewAlpha(image.Rect(0, 0, textW, textH))
		d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(0, ascent)}
		d.DrawString(text)
	}

	var iconImg image.Image
	var iconW, iconH int
	if icon != nil {
		iconH = int(px * 1.3)
		if text == "" {
			iconH = int(px * 2)
		}
		ib := icon.Bounds()
		iconW = max(1, ib.Dx()*iconH/max(1, ib.Dy()))
		scaled := image.NewNRGBA(image.Rect(0, 0, iconW, iconH))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), icon, ib, draw.Src, nil)
		iconImg = scaled
	}

	gap := 0
	if iconImg != nil && mask != nil {
		gap = int(px * 0.4)
	}
	w, h := iconW+gap+textW, max(iconH, textH)
	if w+2*margin > b.Dx() || h+2*margin > b.Dy() {
		return nil // 放不下就不画
	}
	x, y := b.Max.X-margin-w, b.Max.Y-margin-h
	if strings.HasSuffix(mk.st.Position, "l") {
		x = b.Min.X + margin
	}
	if strings.HasPrefix(mk.st.Position, "t") {
		y = b.Min.Y + margin
	}

	if iconImg != nil {
		r := image.Rect(x, y+(h-iconH)/2, x+iconW, y+(h-iconH)/2+iconH)
		draw.DrawMask(dst, r, iconImg, image.Point{}, image.NewUniform(color.Alpha{alpha}), image.Point{}, draw.Over)
	}
	if mask != nil {
		tx, ty := x+iconW+gap, y+(h-textH)/2
		off := max(1, int(px/18))
		shadow := image.NewUniform(color.NRGBA{0, 0, 0, uint8(float64(alpha) * 0.6)})
		draw.DrawMask(dst, image.Rect(tx+off, ty+off, tx+off+textW, ty+off+textH), shadow, image.Point{}, mask, image.Point{}, draw.Over)
		fg := image.NewUniform(color.NRGBA{255, 255, 255, alpha})
		draw.DrawMask(dst, image.Rect(tx, ty, tx+textW, ty+textH), fg, image.Point{}, mask, image.Point{}, draw.Over)
	}
	return nil
}

// ---------- Meta 水印（WebP 的 EXIF / XMP 块） ----------

// buildEXIF 生成小端 TIFF：IFD0 里放 Software / Artist / Copyright（UTF-8）。
func buildEXIF(m *MetaSettings) []byte {
	type tag struct {
		id  uint16
		val string
	}
	tags := []tag{{0x0131, "leopic"}}
	if m.Artist != "" {
		tags = append(tags, tag{0x013B, m.Artist})
	}
	if m.Copyright != "" {
		tags = append(tags, tag{0x8298, m.Copyright})
	}
	le := binary.LittleEndian
	n := len(tags)
	dataOff := 8 + 2 + n*12 + 4
	head := make([]byte, dataOff)
	copy(head, "II*\x00")
	le.PutUint32(head[4:], 8)
	le.PutUint16(head[8:], uint16(n))
	var data []byte
	for i, t := range tags {
		v := append([]byte(t.val), 0)
		e := head[10+i*12:]
		le.PutUint16(e[0:], t.id)
		le.PutUint16(e[2:], 2) // ASCII
		le.PutUint32(e[4:], uint32(len(v)))
		if len(v) <= 4 {
			copy(e[8:12], v)
			continue
		}
		le.PutUint32(e[8:], uint32(dataOff+len(data)))
		data = append(data, v...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	return append(head, data...)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// buildXMP 用 Dublin Core 的 creator / rights，多数看图软件按 XMP 显示中文更可靠。
func buildXMP(m *MetaSettings) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:CreatorTool="leopic">`)
	if m.Artist != "" {
		b.WriteString(`<dc:creator><rdf:Seq><rdf:li>` + xmlEscape(m.Artist) + `</rdf:li></rdf:Seq></dc:creator>`)
	}
	if m.Copyright != "" {
		b.WriteString(`<dc:rights><rdf:Alt><rdf:li xml:lang="x-default">` + xmlEscape(m.Copyright) + `</rdf:li></rdf:Alt></dc:rights>`)
	}
	b.WriteString(`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	return b.Bytes()
}

func riffChunk(id string, data []byte) []byte {
	c := make([]byte, 8, 8+len(data)+1)
	copy(c, id)
	binary.LittleEndian.PutUint32(c[4:], uint32(len(data)))
	c = append(c, data...)
	if len(data)%2 == 1 {
		c = append(c, 0)
	}
	return c
}

// addWebPMeta 把 EXIF / XMP 块加进静态 WebP。简单格式（VP8 / VP8L）补一个 VP8X 头。
func addWebPMeta(webpData, exif, xmp []byte, w, h int) ([]byte, error) {
	if len(webpData) < 20 || string(webpData[:4]) != "RIFF" || string(webpData[8:12]) != "WEBP" {
		return nil, errors.New("不是 WebP")
	}
	body := webpData[12:]
	first := string(body[:4])
	var out []byte
	switch first {
	case "VP8X":
		out = append(out, body...)
	case "VP8 ", "VP8L":
		x := make([]byte, 10)
		if first == "VP8L" && len(body) >= 13 && body[12]&0x10 != 0 { // VP8L alpha_is_used
			x[0] |= 0x10
		}
		w1, h1 := w-1, h-1
		x[4], x[5], x[6] = byte(w1), byte(w1>>8), byte(w1>>16)
		x[7], x[8], x[9] = byte(h1), byte(h1>>8), byte(h1>>16)
		out = append(riffChunk("VP8X", x), body...)
	default:
		return nil, errors.New("未知 WebP 块 " + first)
	}
	if len(exif) > 0 {
		out[8] |= 0x08
		out = append(out, riffChunk("EXIF", exif)...)
	}
	if len(xmp) > 0 {
		out[8] |= 0x04
		out = append(out, riffChunk("XMP ", xmp)...)
	}
	res := make([]byte, 12, 12+len(out))
	copy(res, "RIFF")
	binary.LittleEndian.PutUint32(res[4:], uint32(4+len(out)))
	copy(res[8:], "WEBP")
	return append(res, out...), nil
}
