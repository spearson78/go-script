package bin

import "embed"

//go:embed *.wasm
//go:embed *.html
//go:embed *.js
var WebAssetsFS embed.FS
