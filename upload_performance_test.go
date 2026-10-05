package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os/exec"
	"strings"
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
		})
	}
	if _, err := preparePDFImage([]byte("invalid image")); err == nil {
		t.Fatal("accepted invalid image")
	}
}

func TestFFmpegReceiptFormats(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	for _, codec := range []string{"tiff", "webp", "bmp"} {
		t.Run(codec, func(t *testing.T) {
			cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-threads", "1", "-i", "pipe:0", "-frames:v", "1", "-threads", "1", "-c:v", codec, "-f", "image2pipe", "pipe:1")
			cmd.Stdin = bytes.NewReader(source.Bytes())
			data, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			app := queueTestApp(t)
			queueRequest(t, app, data, "1")
			if _, err := processNextJob(app.settings); err != nil {
				t.Fatal(err)
			}
			_, status := jobState(t, app)
			if status != "completed" {
				t.Fatalf("%s conversion status: %s", codec, status)
			}
		})
	}
}

func TestMissingFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := preparePDFImage([]byte("image"))
	if err == nil || !strings.Contains(err.Error(), "FFmpeg conversion failed") {
		t.Fatalf("missing dependency error: %v", err)
	}
}
