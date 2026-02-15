package strings

import (
	"reflect"
	"strings"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "strings",
		Exported: map[string]reflect.Value{
			"ToUpper": reflect.ValueOf(strings.ToUpper),
		},
		ExportedTypes: map[string]reflect.Type{},
	}

	goscript.RegisterPackage(&pkg)

}
