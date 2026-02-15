package time

import (
	"reflect"
	"time"

	goscript "github.com/spearson78/go-script"
)

func init() {

	pkg := goscript.Package{
		Name: "time",
		Exported: map[string]reflect.Value{
			"Millisecond": reflect.ValueOf(time.Millisecond),
		},
		ExportedTypes: map[string]reflect.Type{},
	}

	goscript.RegisterPackage(&pkg)

}
