package io

import (
	"io"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name:     "io",
		Exported: map[string]reflect.Value{},
		ExportedTypes: map[string]reflect.Type{
			"Writer": reflect.TypeFor[io.Writer](),
		},
	}

	goscript.RegisterPackage(&pkg)

}
