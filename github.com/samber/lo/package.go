package lo

import (
	"reflect"

	"github.com/samber/lo"
	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "github.com/samber/lo",
		Exported: map[string]reflect.Value{
			"T2": reflect.ValueOf(lo.T2[any, any]),
		},
		ExportedTypes: map[string]reflect.Type{
			"Tuple2": reflect.TypeFor[lo.Tuple2[any, any]](),
		},
	}

	goscript.RegisterPackage(&pkg)

}
