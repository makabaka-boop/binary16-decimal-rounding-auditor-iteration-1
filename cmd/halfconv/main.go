// Command halfconv converts a JSON batch of decimal strings to exact
// IEEE binary16 bit patterns.
//
// Usage:
//
//	halfconv < request.json
//	halfconv request.json
//	halfconv -            # read standard input explicitly
//
// The request is either {"values": ["0.1", "-2e3", ...]} or a bare
// JSON array of strings. The response lists the four-hex-digit bit
// pattern, the IEEE class (zero/subnormal/normal/infinity) and the
// exact reduced rounding fraction (null for infinities). Rejected
// entries appear with "ok": false and a reason; malformed requests
// exit non-zero.
package main

import (
	"fmt"
	"os"

	"halfconv/internal/app"
)

func main() {
	var in *os.File = os.Stdin
	switch len(os.Args) {
	case 1:
		// default: standard input
	case 2:
		if os.Args[1] != "-" {
			f, err := os.Open(os.Args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "halfconv: %v\n", err)
				os.Exit(1)
			}
			defer f.Close()
			in = f
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: halfconv [batch.json | -]")
		os.Exit(2)
	}
	os.Exit(app.Execute(in, os.Stdout))
}
