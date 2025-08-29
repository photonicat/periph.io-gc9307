module sample

go 1.22

toolchain go1.22.2

replace periph.io/gc9307 => ../..

require (
	periph.io/gc9307 v0.0.0-00010101000000-000000000000
	periph.io/x/conn/v3 v3.7.1
	periph.io/x/host/v3 v3.8.2
)
