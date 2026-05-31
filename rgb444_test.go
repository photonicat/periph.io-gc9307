package gc9307

import (
	"image"
	"image/color"
	"testing"

	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/pin"
	"periph.io/x/conn/v3/spi"
)

type recBus444 struct {
	limit int
	data  []byte
	n     int
}

func (b *recBus444) String() string                { return "rec" }
func (b *recBus444) Duplex() conn.Duplex            { return conn.Full }
func (b *recBus444) MaxTxSize() int                 { return b.limit }
func (b *recBus444) TxPackets(p []spi.Packet) error { return nil }
func (b *recBus444) Tx(w, r []byte) error {
	if len(w) > b.limit {
		panic("Tx over limit")
	}
	b.data = append(b.data, w...)
	b.n++
	return nil
}

type nopPin444 struct{}

func (nopPin444) String() string                        { return "nop" }
func (nopPin444) Name() string                          { return "nop" }
func (nopPin444) Number() int                           { return 0 }
func (nopPin444) Function() string                      { return "" }
func (nopPin444) Halt() error                           { return nil }
func (nopPin444) Out(gpio.Level) error                  { return nil }
func (nopPin444) PWM(gpio.Duty, physic.Frequency) error { return nil }

var _ spi.Conn = (*recBus444)(nil)
var _ gpio.PinOut = nopPin444{}
var _ pin.Pin = nopPin444{}

// TestRGB444PackingAndSize verifies the 12bpp path emits exactly 1.5 bytes per
// pixel and packs the BGR444 nibbles in the order the GC9307 expects.
func TestRGB444PackingAndSize(t *testing.T) {
	w, h := 4, 2 // even width -> all pairs; 8 px -> 12 bytes
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cols := []color.RGBA{
		{0xF0, 0xA0, 0x50, 255}, {0x10, 0x20, 0x30, 255},
		{0xFF, 0x00, 0x00, 255}, {0x00, 0xFF, 0x00, 255},
		{0x00, 0x00, 0xFF, 255}, {0xFF, 0xFF, 0xFF, 255},
		{0x80, 0x40, 0xC0, 255}, {0x11, 0x22, 0x33, 255},
	}
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, cols[i])
			i++
		}
	}
	bus := &recBus444{limit: 4096}
	d := &Device{bus: bus, dcPin: nopPin444{}, pixelFormat: PIXFMT_RGB444}
	d.dmaBuffer = make([]uint8, 4096)
	if err := d.streamImage444(int16(w), int16(h), img, d.dmaBuffer, 2048); err != nil {
		t.Fatal(err)
	}
	if len(bus.data) != 12 { // 8 px * 1.5 bytes
		t.Fatalf("got %d bytes, want 12 (8px * 1.5)", len(bus.data))
	}
	// First triplet: px0=F0A050, px1=102030, BGR444 layout:
	//   byte0 = B0 | (G0>>4) = 0x50 | 0x0A = 0x5A
	//   byte1 = R0 | (B1>>4) = 0xF0 | 0x03 = 0xF3
	//   byte2 = G1 | (R1>>4) = 0x20 | 0x01 = 0x21
	want := []byte{0x5A, 0xF3, 0x21}
	for k := 0; k < 3; k++ {
		if bus.data[k] != want[k] {
			t.Fatalf("triplet byte %d = %#02x, want %#02x", k, bus.data[k], want[k])
		}
	}
}
