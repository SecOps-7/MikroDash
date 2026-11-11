module mikrodash

go 1.27.3

require (
	github.com/coder/websocket v1.8.15
	github.com/evanw/esbuild v0.28.2
	github.com/go-pdf/fpdf v0.9.0
	github.com/go-routeros/routeros/v3 v3.0.1
	github.com/oschwald/maxminddb-golang/v2 v2.9.0
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2
	golang.zx2c4.com/wireguard v0.0.0-20261024220952-b3bc704ef0ee
	modernc.org/sqlite v1.62.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20261111090704-c24795a75477 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// go-routeros v3.0.1 loses a reply that arrives before its tag is registered.
// The patched copy, and why, are in third_party/go-routeros/PATCHES.md.
replace github.com/go-routeros/routeros/v3 => ./third_party/go-routeros
