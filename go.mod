module mikrodash

go 1.27.0

require (
	github.com/coder/websocket v1.8.15
	github.com/evanw/esbuild v0.28.2
	github.com/go-pdf/fpdf v0.9.0
	github.com/go-routeros/routeros/v3 v3.0.1
	github.com/oschwald/maxminddb-golang/v2 v2.6.0
	golang.org/x/crypto v0.57.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	modernc.org/sqlite v1.59.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// go-routeros v3.0.1 loses a reply that arrives before its tag is registered.
// The patched copy, and why, are in third_party/go-routeros/PATCHES.md.
replace github.com/go-routeros/routeros/v3 => ./third_party/go-routeros
