// Package recruitingconsole contains the Staircase recruitment operations UI.
package recruitingconsole

import (
	"embed"
	"io/fs"
)

//go:embed static
var content embed.FS

func Assets() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
