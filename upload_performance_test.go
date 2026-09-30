package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func benchmarkReceiptJPEG(b *testing.B, dimension int) {
	img := image.NewRGBA(image.Rect(0, 0, dimension, dimension))
	for y := 0; y < dimension; y++ {
		for x := 0; x < dimension; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 76}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := preparePDFImage(buf.Bytes()); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkPreparedReceipt(b *testing.B) { benchmarkReceiptJPEG(b, 1600) }
func BenchmarkLargeReceipt(b *testing.B)    { benchmarkReceiptJPEG(b, 3200) }

func TestPreparePDFImage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		transparent   bool
	}{
		{"prepared JPEG", 1600, 800, false}, {"large JPEG", 3200, 800, false}, {"transparent PNG", 40, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, tc.width, tc.height))
			var buf bytes.Buffer
			var err error
			if tc.transparent {
				err = png.Encode(&buf, img)
			} else {
				err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 76})
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := preparePDFImage(buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if result.Width > 1600 || result.Height > 1600 || result.Width*tc.height != result.Height*tc.width {
				t.Fatalf("bad dimensions: %dx%d", result.Width, result.Height)
			}
			decoded, err := jpeg.Decode(bytes.NewReader(result.JPEG))
			if err != nil {
				t.Fatal(err)
			}
			if tc.transparent {
				r, g, b, _ := decoded.At(0, 0).RGBA()
				if r < 65000 || g < 65000 || b < 65000 {
					t.Fatal("transparent background must be white")
				}
			}
			if tc.name == "prepared JPEG" && !bytes.Equal(result.JPEG, buf.Bytes()) {
				t.Fatal("prepared JPEG was recompressed")
			}
		})
	}
	if _, err := preparePDFImage([]byte("invalid image")); err == nil {
		t.Fatal("accepted invalid image")
	}
}
