package iter

import (
	"iter"
	"reflect"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name:     "iter",
		Exported: map[string]reflect.Value{},
		ExportedTypes: map[string]reflect.Type{
			"Seq": reflect.TypeFor[iter.Seq[any]](),
		},
	}

	goscript.RegisterPackage(&pkg)

}
