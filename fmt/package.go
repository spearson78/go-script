package fmt

import (
	"fmt"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "fmt",
		Exported: map[string]reflect.Value{
			"Println": reflect.ValueOf(func(str ...any) {
				fmt.Fprintln(goscript.Out, str...)
			}),
		},
		ExportedTypes: map[string]reflect.Type{},
	}

	goscript.RegisterPackage(&pkg)

}
