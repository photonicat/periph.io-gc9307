# periph.io driver for gc9307 displays

This package provides a hardware driver for gc9307 displays, that can be used with [periph.io](https://periph.io/).
It is based on the driver from the [TinyGo](https://tinygo.org) [driver package](https://github.com/tinygo-org/drivers).


## Installing

```shell
go get github.com/photonicat/periph.io-gc9307
```

## How to use


```go
package main

import (
	gc9307 "github.com/photonicat/periph.io-gc9307"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

func main() {
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

	conn, err := spiPort.Connect(40000*physic.KiloHertz, spi.Mode0, 8)

	if err != nil {
		log.Fatal(err)
	}

	display := gc9307.New(conn,
		gpioreg.ByName("GPIO3"),
		gpioreg.ByName("GPIO0"),
		gpioreg.ByName("GPIO13"),
		gpioreg.ByName("GPIO12"))

	display.Configure(gc9307.Config{
		Width:        240,
		Height:       320,
		Rotation:     gc9307.ROTATION_90,
		RowOffset:    0,
		ColumnOffset: 0,
		FrameRate:    gc9307.FRAMERATE_60,
		VSyncLines:   gc9307.MAX_VSYNC_SCANLINES,
	})

	// test display
	display.EnableBacklight(true)
	displayPNG(display, 0, 0, "example.png")
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
		return
	}
}
```

## License

This project is licensed under the BSD 3-clause license, just like the [Go project](https://golang.org/LICENSE) itself.
