package bytes

import (
	"bytes"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name:     "bytes",
		Exported: map[string]reflect.Value{},
		ExportedTypes: map[string]reflect.Type{
			"Buffer": reflect.TypeFor[bytes.Buffer](),
		},
	}

	goscript.RegisterPackage(&pkg)

}
