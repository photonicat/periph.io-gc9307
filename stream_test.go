package gc9307

import (
	"image"
	"image/color"
	"math/rand"
	"testing"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/pin"
	"periph.io/x/conn/v3/spi"
)

// recordingBus is a fake spi.Conn that concatenates everything written to it.
type recordingBus struct {
	limit int
	data  []byte
	txN   int
}

func (b *recordingBus) String() string         { return "recordingBus" }
func (b *recordingBus) Duplex() conn.Duplex     { return conn.Full }
func (b *recordingBus) MaxTxSize() int          { return b.limit }
func (b *recordingBus) TxPackets(p []spi.Packet) error { return nil }

func (b *recordingBus) Tx(w, r []byte) error {
	if len(w) > b.limit {
		panic("Tx exceeds advertised MaxTxSize limit")
	}
	b.data = append(b.data, w...)
	b.txN++
	return nil
}

// nopPin is a gpio.PinOut that ignores everything.
type nopPin struct{}

func (nopPin) String() string                                   { return "nop" }
func (nopPin) Name() string                                     { return "nop" }
func (nopPin) Number() int                                      { return 0 }
func (nopPin) Function() string                                 { return "" }
func (nopPin) Halt() error                                      { return nil }
func (nopPin) Out(gpio.Level) error                             { return nil }
func (nopPin) PWM(gpio.Duty, physic.Frequency) error            { return nil }

var _ spi.Conn = (*recordingBus)(nil)
var _ gpio.PinOut = nopPin{}
var _ pin.Pin = nopPin{}

// reference reproduces the original (pre-optimisation) conversion: walk the
// image by (col,row) via RGBAAt and pack with RGBATo565BGR, big-endian bytes.
func reference(fb *image.RGBA) []byte {
	b := fb.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, w*h*2)
	for i := 0; i < w*h; i++ {
		col := i % w
		row := i / w
		c565 := RGBATo565BGR(fb.RGBAAt(b.Min.X+col, b.Min.Y+row))
		out = append(out, uint8(c565>>8), uint8(c565))
	}
	return out
}

func newTestDevice(limit int, batchPixels int32) *Device {
	d := &Device{
		bus:         &recordingBus{limit: limit},
		dcPin:       nopPin{},
		batchLength: batchPixels,
	}
	d.dmaBuffer = make([]uint8, batchPixels*2)
	d.buffer = make([]uint8, batchPixels*2)
	return d
}

func makeImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}
	return img
}

// TestStreamImageMatchesReference proves the linear walk produces byte-for-byte
// the same RGB565-BGR stream as the original per-pixel implementation, across
// several sizes and batch sizes (including batches that split mid-row).
func TestStreamImageMatchesReference(t *testing.T) {
	sizes := [][2]int{{172, 266}, {172, 32}, {172, 22}, {1, 1}, {3, 7}, {320, 1}}
	batches := []int32{1, 16, 172, 320, 2048}

	for _, s := range sizes {
		w, h := s[0], s[1]
		img := makeImage(w, h)
		want := reference(img)

		for _, batch := range batches {
			d := newTestDevice(int(batch)*2, batch)
			bus := d.bus.(*recordingBus)

			if err := d.streamImage(int16(w), int16(h), img, d.dmaBuffer, batch); err != nil {
				t.Fatalf("size %dx%d batch %d: streamImage error: %v", w, h, batch, err)
			}

			if len(bus.data) != len(want) {
				t.Fatalf("size %dx%d batch %d: got %d bytes, want %d",
					w, h, batch, len(bus.data), len(want))
			}
			for i := range want {
				if bus.data[i] != want[i] {
					t.Fatalf("size %dx%d batch %d: byte %d = %#02x, want %#02x",
						w, h, batch, i, bus.data[i], want[i])
				}
			}
		}
	}
}

// TestBusMaxTxSize verifies we honour the bus-advertised transfer limit.
func TestBusMaxTxSize(t *testing.T) {
	if got := busMaxTxSize(&recordingBus{limit: 4096}); got != 4096 {
		t.Fatalf("busMaxTxSize = %d, want 4096", got)
	}
	if got := busMaxTxSize(&recordingBus{limit: 0}); got != defaultMaxTxSize {
		t.Fatalf("busMaxTxSize with no limit = %d, want %d", got, defaultMaxTxSize)
	}
}

// BenchmarkStreamImage measures the per-frame conversion+dispatch cost for a
// 172x266 middle frame at the 4096-byte spidev batch ceiling.
func BenchmarkStreamImage(b *testing.B) {
	img := makeImage(172, 266)
	d := newTestDevice(4096, 2048)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus := d.bus.(*recordingBus)
		bus.data = bus.data[:0]
		bus.txN = 0
		_ = d.streamImage(172, 266, img, d.dmaBuffer, 2048)
	}
}

// reference path benchmark for comparison (old div/mod + RGBAAt + 640B batches).
func BenchmarkReferenceConvert(b *testing.B) {
	img := makeImage(172, 266)
	for i := 0; i < b.N; i++ {
		_ = reference(img)
	}
}
