// Package qr renders a QR code as inline SVG on the server, so the
// two-factor setup page needs no JavaScript and no outside service.
package qr

import (
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// SVG encodes text at medium error correction as one path of module
// runs, no quiet zone (the kit's frame supplies it). The path takes the
// current colour, so the kit keeps it black on white in both themes.
func SVG(text string) (string, error) {
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return "", err
	}
	code.DisableBorder = true
	bits := code.Bitmap()
	n := len(bits)
	var d strings.Builder
	for y, row := range bits {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			w := 0
			for x+w < len(row) && row[x+w] {
				w++
			}
			d.WriteString("M" + strconv.Itoa(x) + " " + strconv.Itoa(y) + "h" + strconv.Itoa(w) + "v1h-" + strconv.Itoa(w) + "z")
			x += w
		}
	}
	size := strconv.Itoa(n)
	return `<svg viewBox="0 0 ` + size + " " + size + `" shape-rendering="crispEdges" aria-hidden="true"><path fill="currentColor" d="` + d.String() + `"></path></svg>`, nil
}
