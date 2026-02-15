package context

import (
	"context"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "context",
		Exported: map[string]reflect.Value{
			"Background":  reflect.ValueOf(context.Background),
			"WithTimeout": reflect.ValueOf(context.WithTimeout),
		},
		ExportedTypes: map[string]reflect.Type{
			"Context": reflect.TypeFor[context.Context](),
		},
	}

	goscript.RegisterPackage(&pkg)

}
