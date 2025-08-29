package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	gc9307 "periph.io/gc9307"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const (
	RST_PIN              = "GPIO122"
	DC_PIN               = "GPIO121"
	CS_PIN               = "GPIO13"
	BL_PIN               = "GPIO13" //now we are using pwm control backlight
	PCAT2_LCD_WIDTH      = 172
	PCAT2_LCD_HEIGHT     = 320
	PCAT2_X_OFFSET       = 34
	PCAT2_L_MARGIN       = 8
	PCAT2_R_MARGIN       = 7
	PCAT2_T_MARGIN       = 10
	PCAT2_B_MARGIN       = 7
	PCAT2_TOP_BAR_HEIGHT = 32
	PCAT2_FOOTER_HEIGHT  = 22

	STATE_UNKNOWN  = -1
	STATE_IDLE     = 0
	STATE_ACTIVE   = 1
	STATE_FADE_IN  = 2
	STATE_FADE_OUT = 3
	STATE_OFF      = 4

	DEFAULT_FPS               = 3
	DEFAULT_IDLE_TIMEOUT      = 60 * time.Second
	ON_CHARGING_IDLE_TIMEOUT  = 365 * 86400 * time.Second
	KEYBOARD_DEBOUNCE_TIME    = 200 * time.Millisecond
	OFF_TIMEOUT               = 3 * time.Second
	INTERVAL_SMS_COLLECT      = 60 * time.Second
	INTERVAL_PCAT_WEB_COLLECT = 10 * time.Second // Increased from 5 to 10 seconds to reduce CPU usage

	ETC_USER_CONFIG_PATH = "/etc/pcat2_mini_display-user_config.json"
	ETC_CONFIG_PATH      = "/etc/pcat2_mini_display-config.json"
)

// Backlight control configuration
var (
	mu                   sync.Mutex
	lastLogical          int
	offTimer             *time.Timer
	cfg                  Config
	ZERO_BACKLIGHT_DELAY = 5 * time.Second
)

// Config holds screen brightness configuration
type Config struct {
	ScreenMinBrightness int
	ScreenMaxBrightness int
}

// setBacklight controls the PWM backlight brightness (0-100)
func setBacklight(brightness int) {

	// perform the write
	if err := os.WriteFile("/sys/class/backlight/backlight/brightness", []byte(strconv.Itoa(brightness)), 0644); err != nil {
		log.Printf("backlight write error: %v", err)
	} else {
		//log.Printf("→ physical backlight %d", phys)
	}

}

func main() {
	// Initialize configuration
	cfg = Config{
		ScreenMinBrightness: 50,
		ScreenMaxBrightness: 100,
	}

	// setup board
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}

	// setup connection to display
	spiPort, err := spireg.Open("SPI1.0")
	if err != nil {
		log.Fatal(err)
	}
	defer spiPort.Close()

	conn, err := spiPort.Connect(180000*physic.KiloHertz, spi.Mode0, 8)
	if err != nil {
		log.Fatal(err)
	}

	os.Remove("/tmp/pcat_display_initialized")

	// Setup display.
	display := gc9307.New(conn, gpioreg.ByName(RST_PIN), gpioreg.ByName(DC_PIN), gpioreg.ByName(CS_PIN), gpioreg.ByName(BL_PIN))
	display.Configure(gc9307.Config{
		Width:        PCAT2_LCD_WIDTH,
		Height:       PCAT2_LCD_HEIGHT,
		Rotation:     gc9307.ROTATION_180,
		RowOffset:    0,
		ColumnOffset: PCAT2_X_OFFSET,
		FrameRate:    gc9307.FRAMERATE_60,
		VSyncLines:   gc9307.MAX_VSYNC_SCANLINES,
		UseCS:        false,
		UseDMA:       true, // Enable DMA by default
	})

	// Set backlight using PWM control function
	log.Println("Setting backlight to 80%...")
	setBacklight(80)

	// Display the example.png image
	log.Println("Displaying example.png...")
	displayPNG(display, 0, 0, "example.png")

	// Keep the image displayed for 10 seconds
	log.Println("Image displayed. Keeping backlight on for 10 seconds...")
	time.Sleep(10 * time.Second)

	// Wait a moment to let the timer complete
	time.Sleep(6 * time.Second)
	log.Println("Sample complete.")
}

func displayPNG(display gc9307.Device, x int, y int, filePath string) {
	// read and parse image file
	image.RegisterFormat("png", "png", png.Decode, png.DecodeConfig)
	imgFile, err := os.Open(filePath)
	if err != nil {
		log.Fatal(err)
	}
	defer imgFile.Close()

	img, _, err := image.Decode(imgFile)
	if err != nil {
		log.Fatal(err)
	}

	// convert image to slice of colors
	bounds := img.Bounds()
	width, height := bounds.Max.X, bounds.Max.Y
	buffer := make([]color.RGBA, height*width)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// get pixel color and convert channels from int32 to int8
			r, g, b, a := img.At(x, y).RGBA()
			buffer[y*width+x] = color.RGBA{R: uint8(r / 0x100), G: uint8(g / 0x100), B: uint8(b / 0x100), A: uint8(a / 0x100)}
		}
	}

	// send image buffer to display
	err = display.FillRectangleWithBuffer(int16(x), int16(y), int16(width), int16(height), buffer)
	if err != nil {
		log.Printf("Display error: %v", err)
		return
	}
}
