package main

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
)

type format struct {
	ext  string
	mime string
}

var errUnsupported = errors.New("不支持的文件类型")

// sniff 按内容判断格式，不信任客户端文件名。
func sniff(data []byte) (format, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return format{"jpg", "image/jpeg"}, nil
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return format{"png", "image/png"}, nil
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return format{"gif", "image/gif"}, nil
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return format{"webp", "image/webp"}, nil
	case bytes.HasPrefix(data, []byte("BM")):
		return format{"bmp", "image/bmp"}, nil
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return format{"tiff", "image/tiff"}, nil
	}
	return format{}, errUnsupported
}

// decodeConfig 读取尺寸，失败返回 0。
func decodeConfig(data []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// webpOpts 是生成衍生图的参数；mk / meta 为 nil 表示不加。
type webpOpts struct {
	quality, maxEdge int
	mk               *marker
	meta             *MetaSettings
}

func decodeImage(data []byte, f format) (image.Image, error) {
	if f.ext == "webp" {
		return webp.Decode(bytes.NewReader(data))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// makeWebP 生成衍生图。GIF 返回 nil（保留动画，回落原图）。
func makeWebP(data []byte, f format, o webpOpts) ([]byte, error) {
	if f.ext == "gif" {
		return nil, nil
	}
	img, err := decodeImage(data, f)
	if err != nil {
		return nil, err
	}
	if f.ext == "jpg" {
		img = applyOrientation(img, jpegOrientation(data))
	}
	return encodeNRGBA(fit(img, o.maxEdge), o)
}

// encodeNRGBA 画水印、编码 WebP、写入元数据。img 已缩放到位。
func encodeNRGBA(img image.Image, o webpOpts) ([]byte, error) {
	if o.mk != nil {
		n := toNRGBA(img)
		if err := o.mk.apply(n); err != nil {
			return nil, err
		}
		img = n
	}
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: o.quality, Method: 4}); err != nil {
		return nil, err
	}
	if o.meta == nil {
		return buf.Bytes(), nil
	}
	b := img.Bounds()
	return addWebPMeta(buf.Bytes(), buildEXIF(o.meta), buildXMP(o.meta), b.Dx(), b.Dy())
}

// toNRGBA 返回可写的 NRGBA；已是 NRGBA 且原点为 0 时直接复用。
func toNRGBA(img image.Image) *image.NRGBA {
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min == (image.Point{}) {
		return n
	}
	b := img.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Bounds(), img, b.Min, draw.Src)
	return n
}

func fit(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}
	nw, nh := maxEdge, h*maxEdge/w
	if h > w {
		nw, nh = w*maxEdge/h, maxEdge
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// jpegOrientation 从 JPEG 的 APP1/EXIF 中读取 Orientation，读不到返回 1。
func jpegOrientation(data []byte) int {
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+segLen]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + segLen
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
		u32 = func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 | int(b[3])<<24 }
	case "MM":
		u16 = func(b []byte) int { return int(b[0])<<8 | int(b[1]) }
		u32 = func(b []byte) int { return int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3]) }
	default:
		return 1
	}
	off := u32(t[4:8])
	if off < 0 || off+2 > len(t) {
		return 1
	}
	n := u16(t[off:])
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if u16(t[e:]) == 0x0112 {
			v := u16(t[e+8:])
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation 先转成 NRGBA 再按字节搬像素，避免逐像素 At/Set 的接口开销。
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	src := toNRGBA(img)
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+w*4]
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(dst.Pix[dy*dst.Stride+dx*4:dy*dst.Stride+dx*4+4], row[x*4:x*4+4])
		}
	}
	return dst
}
