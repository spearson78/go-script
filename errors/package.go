package errors

import (
	"errors"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "errors",
		Exported: map[string]reflect.Value{
			"New": reflect.ValueOf(errors.New),
		},
		ExportedTypes: map[string]reflect.Type{},
	}

	goscript.RegisterPackage(&pkg)

}
