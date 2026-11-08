module bot

// A full patch version in the go line is the minimum toolchain: Go refuses anything older. It does not CAP the
// compiler — a newer local Go still wins — so CI pins the exact one through its image (golang:1.27-alpine and
// actions/setup-go), which is why a redundant `toolchain go1.27.3` line is not kept here (go mod tidy removes it).
go 1.27.3

require (
	github.com/go-telegram/bot v1.27.0
	modernc.org/sqlite v1.61.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.49.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
