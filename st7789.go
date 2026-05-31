// Package gc9307 implements a driver for the gc9307 TFT displays, it comes in various screen sizes.
package gc9307 

import (
	"image/color"
	"math"
	"periph.io/x/conn/v3"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/spi"
	"image"
	"time"
	"os"
	"fmt"
	"log"

	"errors"
)

// defaultMaxTxSize is the fallback per-transfer byte limit used when the SPI
// bus does not advertise its own limit. It matches the common Linux spidev
// bufsiz default of 4096 bytes.
const defaultMaxTxSize = 4096

// busMaxTxSize returns the largest single Tx the bus accepts, in bytes. The
// Linux sysfs SPI driver implements conn.Limits and reports the spidev bufsiz;
// when no limit is advertised we fall back to a safe default.
func busMaxTxSize(bus spi.Conn) int32 {
	if l, ok := bus.(conn.Limits); ok {
		if n := l.MaxTxSize(); n > 0 {
			return int32(n)
		}
	}
	return defaultMaxTxSize
}


// Rotation controls the rotation used by the display.
type Rotation uint8

// FrameRate controls the frame rate used by the display.
type FrameRate uint8

// checkDMAAvailability checks if DMA channels are available for SPI transfers
func checkDMAAvailability() error {
	dmaRxPath := "/sys/devices/platform/soc/2ad00000.spi/dma:rx"
	dmaTxPath := "/sys/devices/platform/soc/2ad00000.spi/dma:tx"
	
	if _, err := os.Stat(dmaRxPath); os.IsNotExist(err) {
		return fmt.Errorf("DMA RX channel not found at %s", dmaRxPath)
	}
	
	if _, err := os.Stat(dmaTxPath); os.IsNotExist(err) {
		return fmt.Errorf("DMA TX channel not found at %s", dmaTxPath)
	}
	
	log.Printf("DMA channels found: RX=%s, TX=%s", dmaRxPath, dmaTxPath)
	return nil
}

// Device wraps an SPI connection.
type Device struct {
	bus             spi.Conn
	dcPin           gpio.PinOut
	resetPin        gpio.PinOut
	csPin           gpio.PinOut
	blPin           gpio.PinOut
	usdCSpin        bool
	width           int16
	height          int16
	columnOffsetCfg int16
	rowOffsetCfg    int16
	columnOffset    int16
	rowOffset       int16
	rotation        Rotation
	frameRate       FrameRate
	batchLength     int32
	dmaBuffer       []uint8  // Pre-allocated DMA buffer
	commandBuffer   []uint8  // Pre-allocated command buffer
	isBGR           bool
	vSyncLines      int16
	buffer          []uint8
	initialized     bool
	useDMA          bool
	maxTransferSize int32
	chunkSize       int32

	// pixelFormat selects the on-the-wire color depth (PIXFMT_RGB565 or
	// PIXFMT_RGB444). RGB444 sends 12 bits/pixel (1.5 bytes) instead of 16,
	// trading color depth for ~25% fewer bytes on the SPI bus.
	pixelFormat uint8

	// Cached address window (post-offset coordinates) so that repeated writes
	// to the same rectangle can skip the CASET/RASET transactions.
	windowValid bool
	winX        int16
	winY        int16
	winW        int16
	winH        int16
}

// Pixel formats for Config.PixelFormat.
const (
	PIXFMT_RGB565 = 0 // 16 bits/pixel, 2 bytes (default)
	PIXFMT_RGB444 = 1 // 12 bits/pixel, 1.5 bytes (4096 colors)
)

// Config is the configuration for the display
type Config struct {
	Width        int16
	Height       int16
	Rotation     Rotation
	RowOffset    int16
	ColumnOffset int16
	FrameRate    FrameRate
	VSyncLines   int16
	UseCS        bool
	UseDMA       bool  // Enable DMA transfers (default: true)
	PixelFormat  uint8 // PIXFMT_RGB565 (default) or PIXFMT_RGB444
}

// New creates a new gc9307 connection. The SPI wire must already be configured.
func New(bus spi.Conn, resetPin, dcPin, csPin, blPin gpio.PinOut) Device {
	return Device{
		bus:      bus,
		dcPin:    dcPin,
		resetPin: resetPin,
		csPin:    csPin,
		blPin:    blPin,
	}
}

// Configure initializes the display with default configuration
func (d *Device) Configure(cfg Config) {
	//touch a file to indicate that the display is initialized
	initializedFile := "/tmp/pcat_display_initialized"

	isInitialized := false
	if _, err := os.Stat(initializedFile); err == nil {
		isInitialized = true
	}

	if cfg.Width != 0 {
		d.width = cfg.Width
	} else {
		d.width = 240
	}
	if cfg.Height != 0 {
		d.height = cfg.Height
	} else {
		d.height = 240
	}
	d.usdCSpin = cfg.UseCS
	d.rotation = cfg.Rotation
	d.rowOffsetCfg = cfg.RowOffset
	d.columnOffsetCfg = cfg.ColumnOffset
	d.pixelFormat = cfg.PixelFormat

	if cfg.FrameRate != 0 {
		d.frameRate = cfg.FrameRate
	} else {
		d.frameRate = FRAMERATE_60
	}

	if cfg.VSyncLines >= 2 && cfg.VSyncLines <= 254 {
		d.vSyncLines = cfg.VSyncLines
	} else {
		d.vSyncLines = 16
	}

	// Configure DMA settings - only if explicitly enabled
	d.useDMA = cfg.UseDMA
	if d.useDMA {
		if err := checkDMAAvailability(); err != nil {
			log.Printf("DMA not available, falling back to original mode: %v", err)
			d.useDMA = false
		}
	}
	
	// Set transfer parameters - use original settings when DMA is disabled
	if d.useDMA {
		// The Linux spidev driver enforces a hard per-ioctl limit equal to
		// /sys/module/spidev/parameters/bufsiz (typically 4096 bytes). Sending
		// a larger Tx fails outright, so discover the real limit from the bus
		// and size our batches right up to it. Larger batches mean fewer Tx
		// syscalls per frame, which is the dominant cost when streaming images.
		d.maxTransferSize = busMaxTxSize(d.bus)
		d.chunkSize = 0 // No chunking needed for DMA
		log.Printf("Using DMA mode for display transfers (max transfer %d bytes)", d.maxTransferSize)
	} else {
		// Keep original settings - no special chunking or size limits
		d.maxTransferSize = 0     // No limit for original mode
		d.chunkSize = 0           // No chunking for original mode
		log.Println("Using original transfer mode")
	}

	// Use original batch length calculation
	d.batchLength = int32(d.width)
	if d.height > d.width {
		d.batchLength = int32(d.height)
	}
	d.batchLength += d.batchLength & 1

	d.buffer = make([]uint8, d.batchLength*2)

	// Pre-allocate DMA buffer to avoid runtime allocations. Use the largest
	// batch (in pixels) that still fits inside a single spidev transfer; each
	// RGB565 pixel is 2 bytes, so the cap is maxTransferSize/2 pixels.
	if d.useDMA {
		dmaBatchLength := d.maxTransferSize / 2 // 2 bytes per pixel
		if dmaBatchLength < d.batchLength {
			dmaBatchLength = d.batchLength
		}
		d.dmaBuffer = make([]uint8, dmaBatchLength*2)
	}
	
	// Pre-allocate command buffer for optimized window setup
	d.commandBuffer = make([]uint8, 11) // Max command sequence size

	//check if the display is already initialized
	
	if !isInitialized {
		// Reset the device
		d.resetPin.Out(gpio.High)
		time.Sleep(10 * time.Millisecond)
		d.resetPin.Out(gpio.Low)
		time.Sleep(50 * time.Millisecond)
		d.resetPin.Out(gpio.High)
		time.Sleep(10 * time.Millisecond)

		// Common initialization
		d.Command(SWRESET)                 // Soft reset
		time.Sleep(10 * time.Millisecond) //
		d.Command(SLPOUT)                  // Exit sleep mode
		time.Sleep(10 * time.Millisecond) //
	}

	// Set color mode. Sent unconditionally (even on warm boot) so the wire
	// format always matches d.pixelFormat. 0x55 = 16bpp RGB565, 0x53 = 12bpp
	// RGB444.
	d.Command(COLMOD)
	if d.pixelFormat == PIXFMT_RGB444 {
		d.Data(0x53) // 12-bit color (RGB444)
	} else {
		d.Data(0x55) // 16-bit color (RGB565)
	}
	time.Sleep(10 * time.Millisecond)

	d.SetRotation(d.rotation) // Memory orientation
	
	d.setWindow(0, 0, d.width, d.height)   // Full draw window
	d.FillScreen(color.RGBA{0, 0, 0, 255}) // Clear screen

	
	// Framerate
	//d.Command(FRCTRL2)         // Frame rate for normal mode
	//d.Data(uint8(d.frameRate)) // Default is 60Hz

	// Frame vertical sync and "porch"
	//
	// Front and back porch controls vertical scanline sync time before and after
	// a frame, where memory can be safely written without tearing.
	//
	/* // photonicat2 does not need this
	fp := uint8(d.vSyncLines / 2)         // Split the desired pause half and half
	bp := uint8(d.vSyncLines - int16(fp)) // between front and back porch.


	d.Command(PORCTRL)
	d.Data(bp)   // Back porch 5bit     (0x7F max 0x08 default)
	d.Data(fp)   // Front porch 5bit    (0x7F max 0x08 default)
	d.Data(0x00) // Seprarate porch     (TODO: what is this?)
	d.Data(0x22) // Idle mode porch     (4bit-back 4bit-front 0x22 default)
	d.Data(0x22) // Partial mode porch  (4bit-back 4bit-front 0x22 default)
	*/
	if true {
		d.Command(INVOFF)
		//time.Sleep(10 * time.Millisecond)
		// Ready to display
		//d.Command(INVOFF)                  // Inversion ON
		time.Sleep(10 * time.Millisecond) //

		d.Command(NORON)                  // Normal mode ON
		time.Sleep(10 * time.Millisecond) //

		d.Command(DISPON)                 // Screen ON
		time.Sleep(10 * time.Millisecond) //

		d.blPin.Out(gpio.High) // Backlight ON
	}

	//touch a file to indicate that the display is initialized
	os.Create(initializedFile)
}

// Sync waits for the display to hit the next VSYNC pause
func (d *Device) Sync() {
	d.SyncToScanLine(0)
}

// SyncToScanLine waits for the display to hit a specific scanline
//
// A scanline value of 0 will forward to the beginning of the next VSYNC,
// even if the display is currently in a VSYNC pause.
//
// Syncline values appear to increment once for every two vertical
// lines on the display.
//
// NOTE: Use GetHighestScanLine and GetLowestScanLine to obtain the highest
// and lowest useful values. Values are affected by front and back porch
// vsync settings (derived from VSyncLines configuration option).
func (d *Device) SyncToScanLine(scanline uint16) {
	scan := d.GetScanLine()

	// Sometimes GetScanLine returns erroneous 0 on first call after draw, so double check
	if scan == 0 {
		scan = d.GetScanLine()
	}

	if scanline == 0 {
		// we dont know where we are in an ongoing vsync so go around
		for scan < 1 {
			time.Sleep(1 * time.Millisecond)
			scan = d.GetScanLine()
		}
		for scan > 0 {
			scan = d.GetScanLine()
		}
	} else {
		// go around unless we're very close to the target
		for scan > scanline+4 {
			time.Sleep(1 * time.Millisecond)
			scan = d.GetScanLine()
		}
		for scan < scanline {
			scan = d.GetScanLine()
		}
	}
}

// GetScanLine reads the current scanline value from the display
func (d *Device) GetScanLine() uint16 {
	data := []uint8{0x00, 0x00}
	d.Rx(GSCAN, data)
	return uint16(data[0])<<8 + uint16(data[1])
}

// GetHighestScanLine calculates the last scanline id in the frame before VSYNC pause
func (d *Device) GetHighestScanLine() uint16 {
	// Last scanline id appears to be backporch/2 + 320/2
	return uint16(math.Ceil(float64(d.vSyncLines)/2)/2) + 160
}

// GetLowestScanLine calculate the first scanline id to appear after VSYNC pause
func (d *Device) GetLowestScanLine() uint16 {
	// First scanline id appears to be backporch/2 + 1
	return uint16(math.Ceil(float64(d.vSyncLines)/2)/2) + 1
}

// Display does nothing, there's no buffer as it might be too big for some boards
func (d *Device) Display() error {
	return nil
}

// SetPixel sets a pixel in the screen
func (d *Device) SetPixel(x int16, y int16, c color.RGBA) {
	if x < 0 || y < 0 ||
		(((d.rotation == NO_ROTATION || d.rotation == ROTATION_180) && (x >= d.width || y >= d.height)) ||
			((d.rotation == ROTATION_90 || d.rotation == ROTATION_270) && (x >= d.height || y >= d.width))) {
		return
	}
	d.FillRectangle(x, y, 1, 1, c)
}

// setWindow prepares the screen to be modified at a given rectangle.
//
// The column (CASET) and row (RASET) address windows are cached: when the same
// rectangle is drawn repeatedly (e.g. every frame of a slide animation writes
// the identical middle region) the four CASET/RASET transactions are skipped
// and only RAMWR is re-issued. RAMWR must always be sent because it resets the
// panel's RAM write pointer to the window's top-left corner before pixel data.
func (d *Device) setWindow(x, y, w, h int16) {
	x += d.columnOffset
	y += d.rowOffset

	cmd := d.commandBuffer

	if !d.windowValid || x != d.winX || y != d.winY || w != d.winW || h != d.winH {
		// CASET command + coordinates
		cmd[0] = CASET
		cmd[1] = uint8(x >> 8)
		cmd[2] = uint8(x)
		cmd[3] = uint8((x + w - 1) >> 8)
		cmd[4] = uint8(x + w - 1)

		d.dcPin.Out(gpio.Low) // Command mode
		d.bus.Tx(cmd[:1], nil)
		d.dcPin.Out(gpio.High) // Data mode
		d.bus.Tx(cmd[1:5], nil)

		// RASET command + coordinates
		cmd[0] = RASET
		cmd[1] = uint8(y >> 8)
		cmd[2] = uint8(y)
		cmd[3] = uint8((y + h - 1) >> 8)
		cmd[4] = uint8(y + h - 1)

		d.dcPin.Out(gpio.Low) // Command mode
		d.bus.Tx(cmd[:1], nil)
		d.dcPin.Out(gpio.High) // Data mode
		d.bus.Tx(cmd[1:5], nil)

		d.winX, d.winY, d.winW, d.winH = x, y, w, h
		d.windowValid = true
	}

	// RAMWR command - always sent; it rewinds the RAM pointer to the window
	// origin so the following pixel stream lands correctly.
	cmd[0] = RAMWR
	d.dcPin.Out(gpio.Low) // Command mode
	d.bus.Tx(cmd[:1], nil)
	d.dcPin.Out(gpio.High) // Data mode for following pixel data
}

// invalidateWindow forces the next setWindow to re-send CASET/RASET. Call it
// whenever something other than FillRectangle* may have moved the address
// window (e.g. scroll, rotation change, raw command sequences).
func (d *Device) invalidateWindow() {
	d.windowValid = false
}

// FillRectangle fills a rectangle at a given coordinates with a color
func (d *Device) FillRectangle(x, y, width, height int16, c color.RGBA) error {
	k, i := d.Size()
	if x < 0 || y < 0 || width <= 0 || height <= 0 ||
		x >= k || (x+width) > k || y >= i || (y+height) > i {
		return errors.New("rectangle coordinates outside display area")
	}
	d.setWindow(x, y, width, height)
	c565 := RGBATo565BGR(c)
	c1 := uint8(c565 >> 8)
	c2 := uint8(c565)

	for i := int32(0); i < d.batchLength; i++ {
		d.buffer[i*2] = c1
		d.buffer[i*2+1] = c2
	}
	j := int32(width) * int32(height)
	for j > 0 {
		if j >= d.batchLength {
			d.Tx(d.buffer, false)
		} else {
			d.Tx(d.buffer[:j*2], false)
		}
		j -= d.batchLength
	}
	return nil
}

// FillRectangleWithBuffer fills buffer with a rectangle at a given coordinates.
func (d *Device) FillRectangleWithBuffer(x, y, width, height int16, buffer []color.RGBA) error {
	i, j := d.Size()
	if x < 0 || y < 0 || width <= 0 || height <= 0 ||
		x >= i || (x+width) > i || y >= j || (y+height) > j {
		return errors.New("rectangle coordinates outside display area")
	}
	if int32(width)*int32(height) != int32(len(buffer)) {
		return errors.New("buffer length does not match with rectangle size")
	}
	d.setWindow(x, y, width, height)

	if d.useDMA {
		return d.fillRectangleWithBufferDMA(width, height, buffer)
	} else {
		return d.fillRectangleWithBufferOriginal(width, height, buffer)
	}
}

// fillRectangleWithBufferDMA uses larger batches optimized for DMA
func (d *Device) fillRectangleWithBufferDMA(width, height int16, buffer []color.RGBA) error {
	// For DMA mode, use larger batch sizes but keep the same logic structure as original
	dmaBatchLength := d.batchLength * 4 // Use 4x larger batches for DMA
	maxBatchLength := d.maxTransferSize / 2 // 2 bytes per pixel
	if dmaBatchLength > maxBatchLength {
		dmaBatchLength = maxBatchLength
	}
	
	// Use pre-allocated DMA buffer
	dmaBuffer := d.dmaBuffer
	
	// Start CS transaction for the entire transfer
	d.BeginTransaction()
	
	k := int32(width) * int32(height)
	offset := int32(0)
	for k > 0 {
		currentBatch := dmaBatchLength
		if k < dmaBatchLength {
			currentBatch = k
		}
		
		for i := int32(0); i < currentBatch; i++ {
			if offset+i < int32(len(buffer)) {
				c565 := RGBATo565BGR(buffer[offset+i])
				c1 := uint8(c565 >> 8)
				c2 := uint8(c565)
				dmaBuffer[i*2] = c1
				dmaBuffer[i*2+1] = c2
			}
		}
		
		d.TxWithCS(dmaBuffer[:currentBatch*2], false, false)
		k -= currentBatch
		offset += currentBatch
	}
	
	// End CS transaction
	d.EndTransaction()
	return nil
}

// fillRectangleWithBufferOriginal uses the original transfer logic (no DMA)
func (d *Device) fillRectangleWithBufferOriginal(width, height int16, buffer []color.RGBA) error {
	// Start CS transaction for the entire transfer
	d.BeginTransaction()
	
	k := int32(width) * int32(height)
	offset := int32(0)
	for k > 0 {
		for i := int32(0); i < d.batchLength; i++ {
			if offset+i < int32(len(buffer)) {
				c565 := RGBATo565BGR(buffer[offset+i])
				c1 := uint8(c565 >> 8)
				c2 := uint8(c565)
				d.buffer[i*2] = c1
				d.buffer[i*2+1] = c2
			}
		}
		if k >= d.batchLength {
			d.TxWithCS(d.buffer, false, false)
		} else {
			d.TxWithCS(d.buffer[:k*2], false, false)
		}
		k -= d.batchLength
		offset += d.batchLength
	}
	
	// End CS transaction
	d.EndTransaction()
	return nil
}

// FillRectangleWithImage fills a rectangle on the display using an *image.RGBA as the framebuffer.
// It assumes that fb's dimensions (Dx x Dy) exactly match the given width and height.
func (d *Device) FillRectangleWithImage(x, y, width, height int16, fb *image.RGBA) error {
	// Get the display size.
	i, j := d.Size()
	if x < 0 || y < 0 || width <= 0 || height <= 0 ||
		x >= i || (x+width) > i || y >= j || (y+height) > j {
		return errors.New("rectangle coordinates outside display area")
	}

	// Verify that the image dimensions match the specified rectangle size.
	if int16(fb.Bounds().Dx()) != width || int16(fb.Bounds().Dy()) != height {
		return errors.New("image dimensions do not match rectangle size")
	}

	// Set the display window to the target rectangle.
	d.setWindow(x, y, width, height)

	if d.useDMA {
		return d.fillRectangleWithImageDMA(width, height, fb)
	} else {
		return d.fillRectangleWithImageOriginal(width, height, fb)
	}
}

// fillRectangleWithImageDMA streams an image to the panel using the largest
// SPI transfers the kernel allows, minimising the number of Tx ioctl syscalls.
func (d *Device) fillRectangleWithImageDMA(width, height int16, fb *image.RGBA) error {
	batchLen := int32(len(d.dmaBuffer) / 2)
	if batchLen <= 0 {
		batchLen = d.batchLength
	}
	return d.streamImage(width, height, fb, d.dmaBuffer, batchLen)
}

// fillRectangleWithImageOriginal uses the original (smaller) batch buffer but
// still benefits from the fast linear pixel walk in streamImage.
func (d *Device) fillRectangleWithImageOriginal(width, height int16, fb *image.RGBA) error {
	return d.streamImage(width, height, fb, d.buffer, d.batchLength)
}

// streamImage converts an *image.RGBA to RGB565 (BGR order) and pushes it to
// the panel in batches of at most batchLen pixels, reusing the provided scratch
// buffer (which must hold at least batchLen*2 bytes).
//
// The hot path walks fb.Pix linearly, four bytes (one RGBA pixel) at a time,
// instead of deriving a row/column and calling fb.RGBAAt() per pixel. That
// removes an integer divide, a modulo and a bounds-checked accessor from the
// inner loop. The RGB565-BGR conversion is inlined so the whole loop is just a
// handful of byte ops plus a store. The DC line is set once for the entire
// stream rather than per batch.
func (d *Device) streamImage(width, height int16, fb *image.RGBA, scratch []uint8, batchLen int32) error {
	if d.pixelFormat == PIXFMT_RGB444 {
		return d.streamImage444(width, height, fb, scratch, batchLen)
	}

	d.BeginTransaction()

	// Hold the data line high for the whole pixel stream; touching the DC GPIO
	// once instead of once per batch removes a syscall-equivalent per transfer.
	d.dcPin.Out(gpio.High)

	pix := fb.Pix
	stride := fb.Stride
	b := fb.Bounds()
	rowBytes := int(width) * 4 // source bytes per visible row (RGBA)

	batch := int(batchLen)
	if batch <= 0 {
		batch = int(width)
	}
	maxOut := batch * 2 // bytes per full batch (2 bytes/pixel)

	out := 0
	rowStart := b.Min.Y*stride + b.Min.X*4
	for row := 0; row < int(height); row++ {
		sp := rowStart
		end := rowStart + rowBytes
		for sp < end {
			r := pix[sp]
			g := pix[sp+1]
			bl := pix[sp+2]
			// BGR565: high byte = bbbbbggg, low byte = gggrrrrr.
			scratch[out] = (bl & 0xF8) | (g >> 5)
			scratch[out+1] = ((g << 3) & 0xE0) | (r >> 3)
			out += 2
			sp += 4

			if out == maxOut {
				d.bus.Tx(scratch[:out], nil)
				out = 0
			}
		}
		rowStart += stride
	}
	// Flush the final partial batch, if any.
	if out > 0 {
		d.bus.Tx(scratch[:out], nil)
	}

	// End CS transaction
	d.EndTransaction()
	return nil
}

// streamImage444 streams an image as 12-bit RGB444 (BGR order to match the
// panel's BGR wiring): two pixels pack into three bytes, so 25% fewer bytes go
// over the SPI bus than RGB565. Each pixel contributes 4-bit B,G,R nibbles;
// the byte layout the GC9307 expects for a pixel pair (P0,P1) is:
//
//	byte0 = B0 G0      (B0 high nibble, G0 low nibble)
//	byte1 = R0 B1
//	byte2 = G1 R1
//
// Pixels are paired within a row (region widths here are even). If a row has an
// odd pixel count the trailing pixel is emitted as 1.5 bytes by flushing its
// half-byte into a final byte; callers use even widths so this path is rare.
func (d *Device) streamImage444(width, height int16, fb *image.RGBA, scratch []uint8, batchLen int32) error {
	d.BeginTransaction()
	d.dcPin.Out(gpio.High)

	pix := fb.Pix
	stride := fb.Stride
	b := fb.Bounds()
	w := int(width)
	rowBytes := w * 4

	// Keep batches a multiple of 3 bytes so a flush never splits a pixel-pair
	// triplet across transfers.
	batchBytes := int(batchLen) * 2
	if batchBytes < 3 {
		batchBytes = 3
	}
	maxOut := (batchBytes / 3) * 3
	if maxOut > len(scratch) {
		maxOut = (len(scratch) / 3) * 3
	}

	out := 0
	rowStart := b.Min.Y*stride + b.Min.X*4
	for row := 0; row < int(height); row++ {
		sp := rowStart
		end := rowStart + rowBytes
		// Process full pixel pairs.
		for sp+8 <= end {
			r0, g0, b0 := pix[sp], pix[sp+1], pix[sp+2]
			r1, g1, b1 := pix[sp+4], pix[sp+5], pix[sp+6]
			scratch[out] = (b0 & 0xF0) | (g0 >> 4)
			scratch[out+1] = (r0 & 0xF0) | (b1 >> 4)
			scratch[out+2] = (g1 & 0xF0) | (r1 >> 4)
			out += 3
			sp += 8
			if out == maxOut {
				d.bus.Tx(scratch[:out], nil)
				out = 0
			}
		}
		// Trailing odd pixel (only if width is odd): emit B,G then R in the
		// high nibble of a third byte. Even widths never hit this.
		if sp+4 <= end {
			r0, g0, b0 := pix[sp], pix[sp+1], pix[sp+2]
			scratch[out] = (b0 & 0xF0) | (g0 >> 4)
			scratch[out+1] = (r0 & 0xF0)
			out += 2
			if out >= maxOut-1 {
				d.bus.Tx(scratch[:out], nil)
				out = 0
			}
		}
		rowStart += stride
	}
	if out > 0 {
		d.bus.Tx(scratch[:out], nil)
	}

	d.EndTransaction()
	return nil
}


// DrawFastVLine draws a vertical line faster than using SetPixel
func (d *Device) DrawFastVLine(x, y0, y1 int16, c color.RGBA) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	d.FillRectangle(x, y0, 1, y1-y0+1, c)
}

// DrawFastHLine draws a horizontal line faster than using SetPixel
func (d *Device) DrawFastHLine(x0, x1, y int16, c color.RGBA) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	d.FillRectangle(x0, y, x1-x0+1, 1, c)
}

// FillScreen fills the screen with a given color
func (d *Device) FillScreen(c color.RGBA) {
	if d.rotation == NO_ROTATION || d.rotation == ROTATION_180 {
		d.FillRectangle(0, 0, d.width, d.height, c)
	} else {
		d.FillRectangle(0, 0, d.height, d.width, c)
	}
}

// SetRotation changes the rotation of the device (clock-wise)
func (d *Device) SetRotation(rotation Rotation) {
	madctl := uint8(0)
	switch rotation % 4 {
	case 0:
		madctl = MADCTL_MX | MADCTL_MY
		d.rowOffset = d.rowOffsetCfg
		d.columnOffset = d.columnOffsetCfg
		break
	case 1:
		madctl = MADCTL_MY | MADCTL_MV
		d.rowOffset = d.columnOffsetCfg
		d.columnOffset = d.rowOffsetCfg
		break
	case 2:
		madctl = MADCTL_MX
		d.rowOffset = 0
		d.columnOffset = d.columnOffsetCfg
		break
	case 3:
		madctl = MADCTL_MX | MADCTL_MV
		d.rowOffset = 0
		d.columnOffset = 0
		break
	}
	if d.isBGR {
		madctl |= MADCTL_BGR
	}
	d.Command(MADCTL)
	d.Data(madctl)
	// Orientation/offsets changed; the cached window is no longer valid.
	d.invalidateWindow()
}

// Command sends a command to the display.
func (d *Device) Command(command uint8) {
	d.Tx([]byte{command}, true)
}

// Data sends data to the display.
func (d *Device) Data(data uint8) {
	d.Tx([]byte{data}, false)
}

// Tx sends data to the display
func (d *Device) Tx(data []byte, isCommand bool) {
	d.TxWithCS(data, isCommand, true)
}

// TxWithCS sends data to the display (CS parameter ignored for performance)
func (d *Device) TxWithCS(data []byte, isCommand bool, toggleCS bool) {
	if isCommand {
		d.dcPin.Out(gpio.Low)
	} else {
		d.dcPin.Out(gpio.High)
	}
	d.bus.Tx(data, nil)
}

// BeginTransaction starts a CS transaction (no-op for performance)
func (d *Device) BeginTransaction() {
	// No CS operations needed - UseCS is always false
}

// EndTransaction ends a CS transaction (no-op for performance)
func (d *Device) EndTransaction() {
	// No CS operations needed - UseCS is always false
}

// Rx reads data from the display
func (d *Device) Rx(command uint8, data []byte) {
	d.dcPin.Out(gpio.Low)
	sendCommand(d.bus, command)

	d.dcPin.Out(gpio.High)
	for i := range data {
		data[i], _ = sendCommand(d.bus, 0xFF)
	}
}

func sendCommand(bus spi.Conn, command byte) (byte, error) {
	err := bus.Tx([]byte{command}, nil)
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// Size returns the current size of the display.
func (d *Device) Size() (w, h int16) {
	if d.rotation == NO_ROTATION || d.rotation == ROTATION_180 {
		return d.width, d.height
	}
	return d.height, d.width
}

// EnableBacklight enables or disables the backlight
func (d *Device) EnableBacklight(enable bool) {
	if enable {
		d.blPin.Out(gpio.High)
	} else {
		d.blPin.Out(gpio.Low)
	}
}

// InvertColors inverts the colors of the screen
func (d *Device) InvertColors(invert bool) {
	if invert {
		d.Command(INVON)
	} else {
		d.Command(INVOFF)
	}
}

// IsBGR changes the color mode (RGB/BGR)
func (d *Device) IsBGR(bgr bool) {
	d.isBGR = bgr
}

// SetScrollArea sets an area to scroll with fixed top and bottom parts of the display.
func (d *Device) SetScrollArea(topFixedArea, bottomFixedArea int16) {
	d.Command(VSCRDEF)
	d.Tx([]uint8{
		uint8(topFixedArea >> 8), uint8(topFixedArea),
		uint8(d.height - topFixedArea - bottomFixedArea>>8), uint8(d.height - topFixedArea - bottomFixedArea),
		uint8(bottomFixedArea >> 8), uint8(bottomFixedArea)},
		false)
}

// SetScroll sets the vertical scroll address of the display.
func (d *Device) SetScroll(line int16) {
	d.Command(VSCRSADD)
	d.Tx([]uint8{uint8(line >> 8), uint8(line)}, false)
	d.invalidateWindow()
}

// StopScroll returns the display to its normal state.
func (d *Device) StopScroll() {
	d.Command(NORON)
}

// RGBATo565 converts a color.RGBA to uint16 used in the display
func RGBATo565(c color.RGBA) uint16 {
	// Convert from 8-bit color channels to 5/6-bit format for RGB565
	r := (uint16(c.R) >> 3) & 0x1F  // 5 bits for red
	g := (uint16(c.G) >> 2) & 0x3F  // 6 bits for green
	b := (uint16(c.B) >> 3) & 0x1F  // 5 bits for blue
	return (r << 11) | (g << 5) | b
}

// RGBATo565BGR converts a color.RGBA to uint16 used in the display (BGR format)
func RGBATo565BGR(c color.RGBA) uint16 {
	// Convert from 8-bit color channels to 5/6-bit format for BGR565
	r := (uint16(c.R) >> 3) & 0x1F  // 5 bits for red
	g := (uint16(c.G) >> 2) & 0x3F  // 6 bits for green
	b := (uint16(c.B) >> 3) & 0x1F  // 5 bits for blue
	return (b << 11) | (g << 5) | r
}
