package strconv

import (
	"reflect"
	"strconv"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "strconv",
		Exported: map[string]reflect.Value{
			"ParseInt": reflect.ValueOf(strconv.ParseInt),
		},
		ExportedTypes: map[string]reflect.Type{},
	}

	goscript.RegisterPackage(&pkg)

}
