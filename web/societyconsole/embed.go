// Package societyconsole contains the local digital-society laboratory UI.
package societyconsole

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
