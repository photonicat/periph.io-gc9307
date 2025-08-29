package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"time"

	gc9307 "periph.io/gc9307"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const (
	RST_PIN         = "GPIO122"
	DC_PIN          = "GPIO121"
	CS_PIN          = "GPIO13"
	BL_PIN          = "GPIO13"
	LCD_WIDTH       = 172
	LCD_HEIGHT      = 320
	X_OFFSET        = 34
	GRID_SIZE       = 3
	DEFAULT_AREA    = 50 // Default 50% area
	MIN_AREA        = 20 // Minimum 20% area
	MAX_AREA        = 100 // Maximum 100% area
)

type BenchmarkApp struct {
	display     gc9307.Device
	imageBuffer []color.RGBA
	imageWidth  int
	imageHeight int
	frameCount  int
	startTime   time.Time
	panX        int
	panY        int
	panDirX     int
	panDirY     int
	maxPanX     int
	maxPanY     int
	useDMA      bool
	areaPercent int
	centerWidth int
	centerHeight int
	startX      int
	startY      int
}

func NewBenchmarkApp(useDMA bool, areaPercent int) *BenchmarkApp {
	return &BenchmarkApp{
		panDirX:     1,
		panDirY:     1,
		useDMA:      useDMA,
		areaPercent: areaPercent,
	}
}

func (app *BenchmarkApp) InitializeDisplay() error {
	if _, err := host.Init(); err != nil {
		return err
	}

	spiPort, err := spireg.Open("SPI1.0")
	if err != nil {
		return err
	}

	conn, err := spiPort.Connect(80000*physic.KiloHertz, spi.Mode0, 8)
	if err != nil {
		return err
	}

	app.display = gc9307.New(conn, gpioreg.ByName(RST_PIN), gpioreg.ByName(DC_PIN), gpioreg.ByName(CS_PIN), gpioreg.ByName(BL_PIN))
	app.display.Configure(gc9307.Config{
		Width:        LCD_WIDTH,
		Height:       LCD_HEIGHT,
		Rotation:     gc9307.ROTATION_180,
		RowOffset:    0,
		ColumnOffset: X_OFFSET,
		FrameRate:    gc9307.FRAMERATE_111,
		VSyncLines:   gc9307.MAX_VSYNC_SCANLINES,
		UseCS:        false,
		UseDMA:       app.useDMA,
	})

	return nil
}

func (app *BenchmarkApp) LoadImage(filePath string) error {
	image.RegisterFormat("png", "png", png.Decode, png.DecodeConfig)
	imgFile, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer imgFile.Close()

	img, _, err := image.Decode(imgFile)
	if err != nil {
		return err
	}

	bounds := img.Bounds()
	app.imageWidth, app.imageHeight = bounds.Max.X, bounds.Max.Y
	app.imageBuffer = make([]color.RGBA, app.imageHeight*app.imageWidth)

	for y := 0; y < app.imageHeight; y++ {
		for x := 0; x < app.imageWidth; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			app.imageBuffer[y*app.imageWidth+x] = color.RGBA{
				R: uint8(r / 0x100),
				G: uint8(g / 0x100),
				B: uint8(b / 0x100),
				A: uint8(a / 0x100),
			}
		}
	}

	// Validate and clamp area percentage
	if app.areaPercent < MIN_AREA {
		app.areaPercent = MIN_AREA
	} else if app.areaPercent > MAX_AREA {
		app.areaPercent = MAX_AREA
	}
	
	// Calculate display area dimensions based on percentage
	areaRatio := float64(app.areaPercent) / 100.0
	app.centerWidth = int(float64(LCD_WIDTH) * areaRatio)
	app.centerHeight = int(float64(LCD_HEIGHT) * areaRatio)
	
	// Ensure area doesn't exceed display bounds
	if app.centerWidth > LCD_WIDTH {
		app.centerWidth = LCD_WIDTH
	}
	if app.centerHeight > LCD_HEIGHT {
		app.centerHeight = LCD_HEIGHT
	}
	
	// Calculate center position on display
	app.startX = (LCD_WIDTH - app.centerWidth) / 2
	app.startY = (LCD_HEIGHT - app.centerHeight) / 2
	
	// Calculate maximum pan range for the specified area
	gridWidth := app.imageWidth * GRID_SIZE
	gridHeight := app.imageHeight * GRID_SIZE
	
	app.maxPanX = gridWidth - app.centerWidth
	app.maxPanY = gridHeight - app.centerHeight
	
	if app.maxPanX < 0 {
		app.maxPanX = 0
	}
	if app.maxPanY < 0 {
		app.maxPanY = 0
	}

	log.Printf("Image loaded: %dx%d, Grid: %dx%d", app.imageWidth, app.imageHeight, gridWidth, gridHeight)
	log.Printf("Display area: %d%% (%dx%d pixels), Position: (%d,%d), Max pan: %dx%d", 
		app.areaPercent, app.centerWidth, app.centerHeight, app.startX, app.startY, app.maxPanX, app.maxPanY)

	return nil
}

func (app *BenchmarkApp) UpdatePanning() {
	// Update pan position pixel by pixel
	app.panX += app.panDirX
	app.panY += app.panDirY

	// Bounce off edges
	if app.panX <= 0 {
		app.panX = 0
		app.panDirX = 1
	} else if app.panX >= app.maxPanX {
		app.panX = app.maxPanX
		app.panDirX = -1
	}

	if app.panY <= 0 {
		app.panY = 0
		app.panDirY = 1
	} else if app.panY >= app.maxPanY {
		app.panY = app.maxPanY
		app.panDirY = -1
	}
}

func (app *BenchmarkApp) RenderFrame() error {
	// Create display buffer for specified area
	displayBuffer := make([]color.RGBA, app.centerWidth*app.centerHeight)

	// Render 3x3 grid with panning offset
	for dy := 0; dy < app.centerHeight; dy++ {
		for dx := 0; dx < app.centerWidth; dx++ {
			// Calculate source position in the 3x3 grid with panning
			srcX := (dx + app.panX) % (app.imageWidth * GRID_SIZE)
			srcY := (dy + app.panY) % (app.imageHeight * GRID_SIZE)
			
			// Determine position within the grid cell
			cellX := srcX % app.imageWidth
			cellY := srcY % app.imageHeight
			
			// Get color from source image
			if cellX < app.imageWidth && cellY < app.imageHeight {
				srcIndex := cellY*app.imageWidth + cellX
				displayBuffer[dy*app.centerWidth+dx] = app.imageBuffer[srcIndex]
			}
		}
	}

	// Send buffer to display
	err := app.display.FillRectangleWithBuffer(
		int16(app.startX), int16(app.startY),
		int16(app.centerWidth), int16(app.centerHeight),
		displayBuffer,
	)
	
	return err
}

func (app *BenchmarkApp) RunBenchmark(durationSeconds int) {
	log.Printf("Starting benchmark for %d seconds...", durationSeconds)
	
	app.frameCount = 0
	app.startTime = time.Now()
	
	ticker := time.NewTicker(16 * time.Millisecond) // ~60 FPS target
	defer ticker.Stop()
	
	endTime := app.startTime.Add(time.Duration(durationSeconds) * time.Second)
	
	for time.Now().Before(endTime) {
		select {
		case <-ticker.C:
			app.UpdatePanning()
			
			if err := app.RenderFrame(); err != nil {
				log.Printf("Render error: %v", err)
				continue
			}
			
			app.frameCount++
			
			// Print FPS every second
			elapsed := time.Since(app.startTime)
			if elapsed >= time.Second && app.frameCount%60 == 0 {
				fps := float64(app.frameCount) / elapsed.Seconds()
				fmt.Printf("FPS: %.2f (Frame: %d, Pan: %d,%d)\n", fps, app.frameCount, app.panX, app.panY)
			}
		}
	}
	
	// Final statistics
	totalElapsed := time.Since(app.startTime)
	avgFPS := float64(app.frameCount) / totalElapsed.Seconds()
	
	fmt.Printf("\nBenchmark completed:\n")
	fmt.Printf("Total frames: %d\n", app.frameCount)
	fmt.Printf("Duration: %.2f seconds\n", totalElapsed.Seconds())
	fmt.Printf("Average FPS: %.2f\n", avgFPS)
}

func main() {
	// Command-line flags
	noDMA := flag.Bool("nodma", false, "Disable DMA transfers (default: false, DMA enabled)")
	duration := flag.Int("duration", 30, "Benchmark duration in seconds")
	area := flag.Int("area", DEFAULT_AREA, fmt.Sprintf("Display area percentage (%d-%d%%, default: %d%%)", MIN_AREA, MAX_AREA, DEFAULT_AREA))
	flag.Parse()

	// Validate area percentage
	if *area < MIN_AREA || *area > MAX_AREA {
		log.Printf("Warning: Area %d%% out of range, clamping to %d-%d%%", *area, MIN_AREA, MAX_AREA)
		if *area < MIN_AREA {
			*area = MIN_AREA
		} else {
			*area = MAX_AREA
		}
	}

	useDMA := !*noDMA
	log.Printf("Starting GC9307 benchmark (DMA: %t, Area: %d%%, Duration: %ds)", useDMA, *area, *duration)
	
	app := NewBenchmarkApp(useDMA, *area)
	
	log.Println("Initializing display...")
	if err := app.InitializeDisplay(); err != nil {
		log.Fatal("Failed to initialize display:", err)
	}
	
	log.Println("Loading example.png...")
	if err := app.LoadImage("example.png"); err != nil {
		log.Fatal("Failed to load image:", err)
	}
	
	log.Println("Display initialized, starting benchmark...")
	
	// Run benchmark for specified duration
	app.RunBenchmark(*duration)
	
	log.Println("Benchmark finished.")
}
