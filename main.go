package goscript

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"iter"
	"path"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/samber/lo"
	"golang.org/x/exp/constraints"
)

var Out io.Writer

var knownPackages map[string]*Package = map[string]*Package{}

type GenericFunction func(typeParams []reflect.Type) reflect.Value

func Coerce[A any](c *runScope, source any) A {
	checkCtxErr(c)

	ret, ok := SafeCoerce[A](c, source)
	if !ok {
		panic(fmt.Errorf("coerce failed %T -> %v", source, reflect.TypeFor[A]()))
	}
	return ret

}

func SafeCoerce[A any](c *runScope, source any) (A, bool) {
	checkCtxErr(c)
	if source == nil {
		var a A
		return a, true
	}
	val, ok := ReflectCoerce(c, reflect.ValueOf(source), reflect.TypeFor[A]())
	if !ok {
		var a A
		return a, false
	}

	if !val.IsValid() || (isNillable(val.Type()) && val.IsNil()) {
		var a A
		return a, true
	} else {
		a, ok := val.Interface().(A)
		if !ok {
			panic("Coerce returned true but cast failed")
		}
		return a, ok
	}

}

func isNillable(t reflect.Type) bool {
	switch t.Kind() {
	//chan, func, interface, map, pointer, or slice
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	default:
		return false
	}
}

func ReflectCoerce(c *runScope, sourceVal reflect.Value, targetType reflect.Type) (reflect.Value, bool) {
	checkCtxErr(c)

	if !sourceVal.IsValid() || (isNillable(sourceVal.Type()) && sourceVal.IsNil()) {
		return reflect.Zero(targetType), true
	}

	if targetType == sourceVal.Type() {
		return sourceVal, true
	}

	if sourceVal.Type().ConvertibleTo(targetType) {
		return sourceVal.Convert(targetType), true
	}

	if sourceVal.Kind() == reflect.Interface && sourceVal.Elem().Type().ConvertibleTo(targetType) {
		return sourceVal.Elem().Convert(targetType), true
	}

	if targetType == reflect.TypeFor[any]() {
		return sourceVal.Convert(targetType), true
	}

	if sourceVal.Kind() == reflect.Interface {
		sourceVal = sourceVal.Elem()
		if !sourceVal.IsValid() || (isNillable(sourceVal.Type()) && sourceVal.IsNil()) {
			return reflect.Zero(targetType), true
		}
	}

	if sourceVal.Kind() == reflect.Pointer && targetType.Kind() == reflect.Pointer {
		sourceVal = sourceVal.Elem()

		if !sourceVal.IsValid() || (isNillable(sourceVal.Type()) && sourceVal.IsNil()) {
			return reflect.Zero(targetType), true
		}

		ret, ok := ReflectCoerce(c, sourceVal, targetType.Elem())
		if !ok {
			return ret, ok
		}

		ptrRet := reflect.New(targetType.Elem())
		ptrRet.Elem().Set(ret)

		return ptrRet, ok
	} else {

		if sourceVal.Type().ConvertibleTo(targetType) {
			return sourceVal.Convert(targetType), true
		}

		if sourceVal.Type().ConvertibleTo(reflect.TypeFor[anyFuncWrapper]()) {
			return sourceVal.FieldByName("anyFunc"), true
		}

		if sourceVal.Type().ConvertibleTo(reflect.TypeFor[scriptFunc]()) {
			if c == nil {
				return reflect.Zero(targetType), false
			}
			scriptFnc := sourceVal.Interface().(scriptFunc)
			return makeFunc(c, targetType, scriptFnc), true
		}

		sourceVal = transform(sourceVal, targetType)
		if sourceVal.Type().ConvertibleTo(targetType) {
			return sourceVal.Convert(targetType), true
		}

		kind := sourceVal.Kind()
		switch kind {
		case 0:
			return reflect.Zero(targetType), true
		case reflect.Slice:

			aType := targetType
			if aType.Kind() == reflect.Slice {
				reslice := reflect.MakeSlice(aType, 0, sourceVal.Len())
				for i := 0; i < sourceVal.Len(); i++ {
					checkCtxErr(c)
					val, ok := ReflectCoerce(c, sourceVal.Index(i), aType.Elem())
					if !ok {
						return reflect.Zero(targetType), false
					}

					reslice = reflect.Append(reslice, val)
				}
				return reslice, true
			} else {
				//Could not coerce
				return reflect.Zero(targetType), false
			}

		case reflect.Map:

			aType := targetType
			if aType.Kind() == reflect.Map {
				remap := reflect.MakeMapWithSize(aType, sourceVal.Len())
				iter := sourceVal.MapRange()
				for iter.Next() {
					checkCtxErr(c)
					k := iter.Key()
					v := iter.Value()

					k, ok := ReflectCoerce(c, k, aType.Key())
					if !ok {
						return reflect.Zero(targetType), false
					}

					v, ok = ReflectCoerce(c, v, aType.Elem())
					if !ok {
						return reflect.Zero(targetType), false
					}

					remap.SetMapIndex(k, v)
				}
				return remap, true
			} else {
				//Could not coerce
				return reflect.Zero(targetType), false
			}
		case reflect.Func:
			if targetType.Kind() == reflect.Func {
				if c == nil {
					return reflect.Zero(targetType), false
				}
				return makeTargetFunc(c, sourceVal, targetType), true
			} else {
				//Could not coerce
				return reflect.Zero(targetType), false
			}
		case reflect.Chan, reflect.Interface, reflect.Ptr:

			//Could not coerce
			return reflect.Zero(targetType), false

		default:
			//Could not coerce
			return reflect.Zero(targetType), false
		}
	}

}

type anyFuncWrapper struct {
	anyFunc  reflect.Value
	origFunc reflect.Value
}

func makeTargetFunc(c *runScope, origFnc reflect.Value, targetFncType reflect.Type) reflect.Value {
	checkCtxErr(c)

	targetFnc := reflect.MakeFunc(
		targetFncType,
		func(targetArgs []reflect.Value) (results []reflect.Value) {
			if origFnc.Type().IsVariadic() {

				var tArgs []reflect.Value
				numArgs := origFnc.Type().NumIn()
				for argNum := 0; argNum < numArgs-2; argNum++ {
					checkCtxErr(c)
					targetArg := targetArgs[argNum]
					targetArg, ok := ReflectCoerce(c, targetArg, origFnc.Type().In(argNum))
					if !ok {
						panic(fmt.Errorf("target func: could not convert %v to %v", targetArg.Type(), origFnc.Type().In(argNum)))
					}
					tArgs = append(tArgs, targetArg)
				}

				variadicType := origFnc.Type().In(numArgs - 1)
				varArgs := targetArgs[numArgs-1]

				tVarArgs := reflect.MakeSlice(variadicType, 0, varArgs.Len()-(numArgs-1))
				for argNum := numArgs - 1; argNum < varArgs.Len(); argNum++ {
					checkCtxErr(c)
					targetArg := varArgs.Index(argNum)
					targetArg, ok := ReflectCoerce(c, targetArg, variadicType.Elem())
					if !ok {
						panic(fmt.Errorf("target func: could not convert %v to %v", targetArg.Type(), variadicType.Elem()))
					}

					tVarArgs = reflect.Append(tVarArgs, targetArg)
				}
				tArgs = append(tArgs, tVarArgs)

				origRets := origFnc.CallSlice(tArgs)

				var targetRets []reflect.Value
				for i, oRet := range origRets {
					checkCtxErr(c)
					targetRet, ok := ReflectCoerce(c, oRet, targetFncType.Out(i))
					if !ok {
						panic(fmt.Errorf("target func ret: could not convert %v to %v", oRet.Type(), targetFncType.Out(i)))
					}
					targetRets = append(targetRets, targetRet)
				}

				return targetRets
			} else {

				var tArgs []reflect.Value
				numArgs := origFnc.Type().NumIn()
				for argNum := 0; argNum < numArgs; argNum++ {
					checkCtxErr(c)
					targetArg := targetArgs[argNum]
					targetArg, ok := ReflectCoerce(c, targetArg, origFnc.Type().In(argNum))
					if !ok {
						panic(fmt.Errorf("target func: could not convert %v to %v", targetArg.Type(), origFnc.Type().In(argNum)))
					}
					tArgs = append(tArgs, targetArg)
				}

				origRets := origFnc.Call(tArgs)

				var targetRets []reflect.Value
				for i, oRet := range origRets {
					checkCtxErr(c)
					targetRet, ok := ReflectCoerce(c, oRet, targetFncType.Out(i))
					if !ok {
						panic(fmt.Errorf("target func ret: could not convert %v to %v", oRet.Type(), targetFncType.Out(i)))
					}
					targetRets = append(targetRets, targetRet)
				}

				return targetRets
			}
		},
	)

	return reflect.ValueOf(anyFuncWrapper{
		anyFunc:  targetFnc,
		origFunc: origFnc,
	})

}

var variadicFuncTypes = [][]reflect.Type{
	{
		reflect.TypeFor[func()](),
		reflect.TypeFor[func() any](),
		reflect.TypeFor[func() (any, any)](),
		reflect.TypeFor[func() (any, any, any)](),
	},
	{
		reflect.TypeFor[func(...any)](),
		reflect.TypeFor[func(...any) any](),
		reflect.TypeFor[func(...any) (any, any)](),
		reflect.TypeFor[func(...any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, ...any)](),
		reflect.TypeFor[func(any, ...any) any](),
		reflect.TypeFor[func(any, ...any) (any, any)](),
		reflect.TypeFor[func(any, ...any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, ...any)](),
		reflect.TypeFor[func(any, any, ...any) any](),
		reflect.TypeFor[func(any, any, ...any) (any, any)](),
		reflect.TypeFor[func(any, any, ...any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, ...any)](),
		reflect.TypeFor[func(any, any, any, ...any) any](),
		reflect.TypeFor[func(any, any, any, ...any) (any, any)](),
		reflect.TypeFor[func(any, any, any, ...any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, any, ...any)](),
		reflect.TypeFor[func(any, any, any, any, ...any) any](),
		reflect.TypeFor[func(any, any, any, any, ...any) (any, any)](),
		reflect.TypeFor[func(any, any, any, any, ...any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, any, any, ...any)](),
		reflect.TypeFor[func(any, any, any, any, any, ...any) any](),
		reflect.TypeFor[func(any, any, any, any, any, ...any) (any, any)](),
		reflect.TypeFor[func(any, any, any, any, any, ...any) (any, any, any)](),
	},
}

var funcTypes = [][]reflect.Type{
	{
		reflect.TypeFor[func()](),
		reflect.TypeFor[func() any](),
		reflect.TypeFor[func() (any, any)](),
		reflect.TypeFor[func() (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any)](),
		reflect.TypeFor[func(any) any](),
		reflect.TypeFor[func(any) (any, any)](),
		reflect.TypeFor[func(any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any)](),
		reflect.TypeFor[func(any, any) any](),
		reflect.TypeFor[func(any, any) (any, any)](),
		reflect.TypeFor[func(any, any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any)](),
		reflect.TypeFor[func(any, any, any) any](),
		reflect.TypeFor[func(any, any, any) (any, any)](),
		reflect.TypeFor[func(any, any, any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, any)](),
		reflect.TypeFor[func(any, any, any, any) any](),
		reflect.TypeFor[func(any, any, any, any) (any, any)](),
		reflect.TypeFor[func(any, any, any, any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, any, any)](),
		reflect.TypeFor[func(any, any, any, any, any) any](),
		reflect.TypeFor[func(any, any, any, any, any) (any, any)](),
		reflect.TypeFor[func(any, any, any, any, any) (any, any, any)](),
	},
	{
		reflect.TypeFor[func(any, any, any, any, any, any)](),
		reflect.TypeFor[func(any, any, any, any, any, any) any](),
		reflect.TypeFor[func(any, any, any, any, any, any) (any, any)](),
		reflect.TypeFor[func(any, any, any, any, any, any) (any, any, any)](),
	},
}

func getAnyFncType(fncType reflect.Type) reflect.Type {

	if fncType.IsVariadic() {

		if fncType.NumIn() >= len(variadicFuncTypes) {
			panic(fmt.Errorf("unsupported variadic parameter return count %v %v", fncType.NumIn(), fncType.NumOut()))
		}

		numOut := variadicFuncTypes[fncType.NumIn()]

		if fncType.NumOut() >= len(numOut) {
			panic(fmt.Errorf("unsupported variadic parameter return count %v %v", fncType.NumIn(), fncType.NumOut()))
		}

		return numOut[fncType.NumOut()]
	} else {

		if fncType.NumIn() >= len(funcTypes) {
			panic(fmt.Errorf("unsupported parameter return count %v %v", fncType.NumIn(), fncType.NumOut()))
		}

		numOut := funcTypes[fncType.NumIn()]

		if fncType.NumOut() >= len(numOut) {
			panic(fmt.Errorf("unsupported parameter return count %v %v", fncType.NumIn(), fncType.NumOut()))
		}

		return numOut[fncType.NumOut()]
	}
}

type Transformer func(reflect.Value, reflect.Type) reflect.Value

var transformers []Transformer

func RegisterTransformer(t Transformer) {
	transformers = append(transformers, t)
}

type TypeEraser func(reflect.Value) reflect.Value

var erasers []TypeEraser

func RegisterEraser(t TypeEraser) {
	erasers = append(erasers, t)
}

func RegisterPackage(p *Package) {
	//TODO: I will need something like this to transform the Optic params and return to be Optic[any].
	/*
		for fncName, fncValue := range p.Exported {

			if fncValue.Kind() == reflect.Func {
				anyFncType := getAnyFncType(fncValue.Type())
				anyFnc := makeAnyFunc(fncValue, anyFncType)
				p.Exported[fncName] = anyFnc
			}

		}
	*/
	knownPackages[p.Name] = p
}

type scriptField struct {
	Name       string
	Type       reflect.Type
	CustomType *scriptCustomType
}

type scriptCustomType struct {
	Kind      reflect.Kind
	Fields    []*scriptField
	Key       reflect.Type
	Elt       reflect.Type
	CustomKey *scriptCustomType
	CustomElt *scriptCustomType
}

type runContext struct {
	Types    map[string]*scriptCustomType
	DotTypes map[string]reflect.Type
	Globals  map[string]reflect.Value
	Imports  map[string]*Package
	Return   []reflect.Value //TODO:needs to be scoped
	Ctx      context.Context
}

type runScope struct {
	ctx       *runContext
	parent    *runScope
	Variables map[string]reflect.Value
	DeferList []ast.Expr
}

func (r *runScope) Resolve(name string) (reflect.Value, bool) {
	checkCtxErr(r)
	val, ok := r.Variables[name]
	if ok {
		return val, true
	}

	if r.parent != nil {
		return r.parent.Resolve(name)
	}

	val, ok = r.ctx.Globals[name]
	return val, ok
}

type Package struct {
	Name          string
	Package       string
	Exported      map[string]reflect.Value
	ExportedTypes map[string]reflect.Type
}

func checkCtxErr(c *runScope) {
	if c == nil || c.ctx == nil || c.ctx.Ctx == nil {
		return
	}

	//Without this the context deadline doesn't seem to expire.
	runtime.Gosched()
	ctxErr := c.ctx.Ctx.Err()
	if ctxErr != nil {
		panic(fmt.Errorf("script execution aborted: %w", ctxErr))
	}
}

func execBlock(c *runScope, b *ast.BlockStmt) (controlFlow, error) {
	checkCtxErr(c)
	for _, stmt := range b.List {
		checkCtxErr(c)
		fc, err := execStmt(c, stmt)
		if err != nil {
			return flowError, err
		}
		if fc != flowContinue {
			return fc, nil
		}
	}
	return flowContinue, nil
}

type controlFlow int

const (
	flowContinue controlFlow = iota
	flowBreak
	flowReturn
	flowError
)

func seq2(v reflect.Value) iter.Seq2[reflect.Value, reflect.Value] {

	seq2T := reflect.TypeFor[iter.Seq2[any, any]]()
	if seq2, ok := ReflectCoerce(nil, v, seq2T); ok {
		return func(yield func(reflect.Value, reflect.Value) bool) {
			seq2.Interface().(iter.Seq2[any, any])(func(i any, v any) bool {
				return yield(reflect.ValueOf(i), reflect.ValueOf(v))
			})
		}
	}

	return v.Seq2()
}

func execStmt(c *runScope, stmt ast.Stmt) (controlFlow, error) {
	checkCtxErr(c)
	switch t := stmt.(type) {
	case *ast.ExprStmt:
		_, err := execExpr(c, t.X, 1, nil)
		return flowContinue, err
	case *ast.AssignStmt:
		if len(t.Lhs) > 1 && len(t.Rhs) == 1 {
			//Expect a a tuple return
			ret, err := execExpr(c, t.Rhs[0], len(t.Lhs), nil)
			if err != nil {
				return flowError, err
			}

			if len(ret) != len(t.Lhs) {
				return flowError, fmt.Errorf("assignment expected %v return values", len(t.Lhs))
			}

			i := 0
			for _, lhs := range t.Lhs {
				checkCtxErr(c)
				val := ret[i]

				if !val.IsValid() {
					//TODO: I don't like this
					ptr := reflect.New(reflect.TypeFor[any]())
					val = ptr.Elem()
				}

				obj, err := execExpr(c, lhs, 1, val.Type())
				if err != nil {
					return flowError, fmt.Errorf("execStmt: %w", err)
				}

				obj[0].Set(val)

				i++
			}
			return flowContinue, nil

		} else {

			if len(t.Lhs) != len(t.Rhs) {
				return flowError, fmt.Errorf("assignment mismatch %v:%v", len(t.Lhs), len(t.Rhs))
			}
			var vals []reflect.Value
			for _, rhs := range t.Rhs {
				checkCtxErr(c)
				ret, err := execExpr(c, rhs, 1, nil)
				if err != nil {
					return flowError, err
				}
				if len(ret) != 1 {
					return flowError, errors.New("assignment expected single return")
				}
				vals = append(vals, ret[0])
			}

			i := 0
			for _, lhs := range t.Lhs {
				checkCtxErr(c)
				val := vals[i]
				//Try to keep variables as their type erased variant.
				val = typeErase(val)

				obj, err := execExpr(c, lhs, 1, val.Type())
				if err != nil {
					return flowError, err
				}
				obj[0].Set(val)

				i++
			}
			return flowContinue, nil
		}
	case *ast.ReturnStmt:
		var rets []reflect.Value
		for _, retExpr := range t.Results {
			checkCtxErr(c)
			exprRets, err := execExpr(c, retExpr, 1, nil)
			if err != nil {
				return flowError, err
			}

			for _, ret := range exprRets {
				checkCtxErr(c)
				if ret.IsValid() && !(isNillable(ret.Type()) && !ret.IsNil()) {
					ptr := reflect.New(ret.Type())
					ptr.Elem().Set(ret)

					rets = append(rets, ptr.Elem())
				} else {
					rets = append(rets, ret)
				}
			}
		}
		//TODO: return needs to be scoped
		c.ctx.Return = rets
		return flowReturn, nil
	case *ast.BranchStmt:
		switch t.Tok {
		case token.BREAK:
			return flowBreak, nil
		case token.CONTINUE:
			return flowContinue, nil
		default:
			return flowError, fmt.Errorf("illegal branch nin range %v", t.Tok)
		}
	case *ast.IfStmt:
		if t.Init != nil {
			_, err := execStmt(c, t.Init) //TODO: scoping
			if err != nil {
				return flowError, err
			}
		}

		ret, err := execExpr(c, t.Cond, 1, nil)
		if err != nil {
			return flowError, err
		}
		if Coerce[bool](c, ret[0].Interface()) {
			fc, err := execStmt(c, t.Body)
			return fc, err
		} else {
			if t.Else != nil {
				fc, err := execStmt(c, t.Else)
				return fc, err
			}
		}

		return flowContinue, nil
	case *ast.BlockStmt:
		var fc controlFlow
		var err error
		for _, stmnt := range t.List {
			checkCtxErr(c)
			fc, err = execStmt(c, stmnt) //TODO: scope
			if err != nil {
				return fc, err
			}
			if fc != flowContinue {
				break
			}
		}
		return fc, nil
	case *ast.RangeStmt:
		rangeOver, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return flowError, err
		}

		forScope := &runScope{
			ctx:       c.ctx,
			parent:    c,
			Variables: make(map[string]reflect.Value, 10),
		}

		if t.Value == nil {
			name, ok := t.Key.(*ast.Ident)
			if !ok {
				return flowError, fmt.Errorf("for expected identifier %T", t.Key)
			}

			for val := range rangeOver[0].Elem().Seq() {
				checkCtxErr(forScope)
				forScope.Variables[name.Name] = val

				flow, err := execBlock(forScope, t.Body)
				if err != nil {
					return flowError, err
				}

				if flow == flowContinue {
					continue
				}

				if flow == flowBreak {
					break
				}

				if flow == flowReturn {
					return flowReturn, nil
				}

			}

			return flowContinue, nil
		} else {

			keyName, ok := t.Key.(*ast.Ident)
			if !ok {
				return flowError, fmt.Errorf("for expected identifier %T", t.Key)
			}
			name, ok := t.Value.(*ast.Ident)
			if !ok {
				return flowError, fmt.Errorf("for expected identifier %T", t.Key)
			}

			for i, val := range seq2(rangeOver[0]) {
				checkCtxErr(c)
				forScope.Variables[keyName.Name] = i
				forScope.Variables[name.Name] = val

				flow, err := execBlock(forScope, t.Body)
				if err != nil {
					return flowError, err
				}

				if flow == flowContinue {
					continue
				}

				if flow == flowBreak {
					break
				}

				if flow == flowReturn {
					return flowReturn, nil
				}

			}

			return flowContinue, nil
		}

	case *ast.DeclStmt:
		return flowContinue, execDecl(c, t.Decl)
	case *ast.DeferStmt:
		c.DeferList = append(c.DeferList, t.Call)
		return flowContinue, nil
	case *ast.ForStmt:

		forScope := &runScope{
			ctx:       c.ctx,
			parent:    c,
			Variables: make(map[string]reflect.Value, 10),
		}

		if t.Init != nil {
			_, err := execStmt(forScope, t.Init)
			if err != nil {
				return flowError, err
			}
		}

		for {
			checkCtxErr(c)
			if t.Cond != nil {
				cond, err := execExpr(c, t.Cond, 1, nil)
				if err != nil {
					return flowError, err
				}

				coerced, ok := ReflectCoerce(c, cond[0], reflect.TypeFor[bool]())
				if !ok {
					return flowError, errors.New("forStmt cond coerce failed")
				}

				if !coerced.Bool() {
					break
				}
			}

			flow, err := execStmt(c, t.Body)
			if err != nil {
				return flowError, err
			}

			if flow == flowContinue {
				continue
			}

			if flow == flowBreak {
				break
			}

			if flow == flowReturn {
				return flowReturn, nil
			}
			if t.Post != nil {
				_, err := execStmt(c, t.Post)
				if err != nil {
					return flowError, err
				}
			}

		}
		return flowContinue, nil
	case *ast.IncDecStmt:
		lhs, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return flowError, err
		}

		switch lhs[0].Type().Kind() {
		case reflect.Int:
			if t.Tok == token.INC {
				lhs[0].Set(reflect.ValueOf(int(lhs[0].Int()) + 1))
			} else {
				lhs[0].Set(reflect.ValueOf(int(lhs[0].Int()) - 1))
			}
			return flowContinue, nil
		case reflect.Float64:
			if t.Tok == token.INC {
				lhs[0].Set(reflect.ValueOf(lhs[0].Float() + 1.0))
			} else {
				lhs[0].Set(reflect.ValueOf(lhs[0].Float() - 1.0))
			}
			return flowContinue, nil
		default:
			return flowError, fmt.Errorf("unknown incdec type %v", lhs[0].Kind())
		}

	default:
		return flowError, fmt.Errorf("unknown statement %T", t)
	}
}

func getType(e ast.Expr) (reflect.Type, error) {

	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "int":
			return reflect.TypeFor[int](), nil
		case "any":
			return reflect.TypeFor[any](), nil
		default:
			return nil, fmt.Errorf("unknown type ident %v", t.Name)
		}
	default:
		return nil, fmt.Errorf("unknown type expression %T", t)
	}

}

func getTypeParams(e ast.Expr) ([]reflect.Type, error) {
	switch t := e.(type) {
	case *ast.Ident:
		return nil, errors.New("cannot infer type params.")
	case *ast.IndexExpr:
		tp, err := getType(t.Index)
		if err != nil {
			return nil, err
		}
		return []reflect.Type{tp}, nil
	case *ast.IndexListExpr:
		var ret []reflect.Type
		for _, i := range t.Indices {
			tp, err := getType(i)
			if err != nil {
				return nil, err
			}
			ret = append(ret, tp)
		}

		return ret, nil
	default:
		return nil, fmt.Errorf("getTypeParams: unknown expr %T", t)
	}
}

func resolveField(c *runScope, v reflect.Value, field *ast.Ident) (reflect.Value, error) {
	checkCtxErr(c)

	if !v.IsValid() || (isNillable(v.Type()) && v.IsNil()) {
		return reflect.Zero(reflect.TypeFor[any]()), fmt.Errorf("reference field %v on nil", field.Name)
	}

	switch t := v.Interface().(type) {
	case *Package:
		val, ok := t.Exported[field.Name]
		if !ok {
			return reflect.ValueOf(nil), fmt.Errorf("resolveField: unknown field %v", field.Name)
		}

		return val, nil
	case scriptObj:
		holder, ok := t[field.Name]
		if !ok {
			return reflect.ValueOf(nil), fmt.Errorf("resolveField: unknown field %v", field.Name)
		}

		return reflect.ValueOf(holder.val).Elem(), nil
	}

	switch v.Kind() {
	case reflect.Interface:
		m := v.Elem().MethodByName(field.Name)
		if m.IsValid() {
			return m, nil
		}
		//TODO: I think interfaces are getting double wrapped somewhere...
		return resolveField(c, v.Elem(), field)
	case reflect.Struct:
		f := v.FieldByName(field.Name)
		if f.IsValid() {
			return f, nil
		}

		m := v.MethodByName(field.Name)
		if m.IsValid() {
			return m, nil
		}

		return reflect.ValueOf(nil), fmt.Errorf("member not found %v", field.Name)
	case reflect.Pointer:
		m := v.MethodByName(field.Name)
		if m.IsValid() {
			return m, nil
		}

		return resolveField(c, v.Elem(), field)
	default:
		return reflect.ValueOf(nil), fmt.Errorf("resolveField: unknown expr %v on %v : %T : %v", field, v.Interface(), v.Interface(), v.Kind())
	}

}

func resolveType(c *runScope, name ast.Expr) (reflect.Type, *scriptCustomType, error) {
	checkCtxErr(c)
	switch t := name.(type) {
	case *ast.Ident:
		switch t.Name {
		//TODO: complete these types
		case "string":
			return reflect.TypeFor[string](), nil, nil
		case "int":
			return reflect.TypeFor[int](), nil, nil
		case "int64":
			return reflect.TypeFor[int64](), nil, nil
		case "float64":
			return reflect.TypeFor[float64](), nil, nil
		case "any":
			return reflect.TypeFor[any](), nil, nil
		default:
			if customType, ok := c.ctx.Types[t.Name]; ok {
				return nil, customType, nil
			}

			if dotType, ok := c.ctx.DotTypes[t.Name]; ok {
				return dotType, nil, nil
			}

			return nil, nil, fmt.Errorf("resolveType: unknown type %v", t.Name)
		}
	case *ast.ArrayType:
		//TODO: arrays as well as slices
		eltType, custType, err := resolveType(c, t.Elt)
		if err != nil {
			return nil, nil, err
		}

		if eltType == nil {
			return nil, &scriptCustomType{
				Kind:      reflect.Slice,
				CustomElt: custType,
			}, nil
		} else {
			return reflect.SliceOf(eltType), nil, nil
		}

	case *ast.MapType:
		valType, valCustType, err := resolveType(c, t.Value)
		if err != nil {
			return nil, nil, err
		}

		keyType, keyCustType, err := resolveType(c, t.Key)
		if err != nil {
			return nil, nil, err
		}

		if valType == nil || keyType == nil {
			return nil, &scriptCustomType{
				Kind:      reflect.Map,
				Key:       keyType,
				Elt:       valType,
				CustomElt: valCustType,
				CustomKey: keyCustType,
			}, nil
		} else {
			return reflect.MapOf(keyType, valType), nil, nil
		}
	case *ast.SelectorExpr:
		pkgName := t.X.(*ast.Ident)
		pkg := c.ctx.Imports[pkgName.Name]
		x := pkg.ExportedTypes[t.Sel.Name]
		return x, nil, nil
	case *ast.StarExpr:
		goType, custType, err := resolveType(c, t.X)
		if err != nil {
			return nil, nil, err
		}

		if goType != nil {
			return reflect.PointerTo(goType), nil, nil
		} else {
			return nil, &scriptCustomType{
				Kind:      reflect.Pointer,
				CustomElt: custType,
			}, nil
		}
	case *ast.FuncType:
		//Todo: this doesn't feel right
		return reflect.TypeFor[any](), nil, nil
	case *ast.IndexListExpr:
		//Type parameters are erased
		return resolveType(c, t.X)
	default:
		return nil, nil, fmt.Errorf("resolveType: unknown expr %T", t)
	}

	/*


		if len(name) == 2 {
			if pkg, ok := c.Imports[name[0]]; ok {
				if t, ok := pkg.ExportedTypes[name[1]]; ok {
					return t, nil
				}
			}

		}
	*/

}

type scriptFunc struct {
	Scope *runScope
	Type  *ast.FuncType  // function type
	Body  *ast.BlockStmt // function body
	//TODO: I think I will need to capture the scope here.
}

type valHolder struct {
	val *any
}

func (s valHolder) String() string {
	return fmt.Sprintf("%v", *s.val)
}

type scriptObj map[string]valHolder

func (s scriptObj) String() string {
	var vals []struct {
		K string
		V any
	}

	for k, v := range s {
		vals = append(vals, struct {
			K string
			V any
		}{k, v})
	}

	sort.Slice(vals, func(i, j int) bool {
		return vals[i].K < vals[j].K
	})

	var sb strings.Builder
	sb.WriteString("{")
	for i, v := range vals {
		if i != 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(fmt.Sprintf("%v", v.V))
	}
	sb.WriteString("}")

	return sb.String()
}

type scriptMap map[any]valHolder

func passByValue(param any) any {
	if scrObj, ok := param.(scriptObj); ok {

		clone := make(scriptObj, len(scrObj))

		for k, v := range scrObj {
			newVal := passByValue(*v.val)
			clone[k] = valHolder{val: &newVal}
		}

		return clone
	} else {
		return param
	}
}

func passByValueReflect(c *runScope, param reflect.Value, argType reflect.Type) (reflect.Value, error) {
	checkCtxErr(c)
	if param.Type().ConvertibleTo(reflect.TypeFor[scriptObj]()) {
		scrObj := param.Interface().(scriptObj)

		clone := make(scriptObj, len(scrObj))

		for k, v := range scrObj {
			checkCtxErr(c)
			newVal := passByValue(*v.val)
			clone[k] = valHolder{val: &newVal}
		}

		return reflect.ValueOf(clone), nil
	} else {
		ptr := reflect.New(argType)
		coerced, ok := ReflectCoerce(c, param, argType)
		if !ok {
			return reflect.Zero(argType), fmt.Errorf("pass by: could not convert %v to %v", param.Type(), argType)
		}
		ptr.Elem().Set(coerced)
		return ptr.Elem(), nil
	}
}

func isNil(t reflect.Value) bool {
	switch t.Type().Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return t.IsNil()
	default:
		return false
	}

}

func makeFunc(c *runScope, funcType reflect.Type, scriptFnc scriptFunc) reflect.Value {
	checkCtxErr(c)
	scriptFncWrapper := reflect.MakeFunc(funcType, func(args []reflect.Value) (results []reflect.Value) {

		c.ctx.Return = nil

		callScope := &runScope{
			ctx:       scriptFnc.Scope.ctx,
			parent:    scriptFnc.Scope,
			Variables: make(map[string]reflect.Value, 10),
		}

		var flattenedParams []lo.Tuple2[string, ast.Expr]

		for _, field := range scriptFnc.Type.Params.List {
			for _, name := range field.Names {
				flattenedParams = append(flattenedParams, lo.T2(name.Name, field.Type))
			}
		}

		for argNum, arg := range args {
			checkCtxErr(c)
			//arg = arg.Convert()
			param := flattenedParams[argNum]
			//TODO: this is wrong need scopes
			//TODO: what about tuple returns...

			goType, custType, err := resolveType(c, param.B)
			if err != nil {
				panic(err) //TODO: handle this panic
			}

			if goType != nil {
				cloneArg, err := passByValueReflect(c, arg, goType)
				if err != nil {
					panic(err)
				}
				callScope.Variables[param.A] = cloneArg
			} else {

				switch custType.Kind {
				case reflect.Pointer:
					callScope.Variables[param.A] = arg.Elem()
				case reflect.Struct:
					scrObj := arg.Interface().(scriptObj)

					clone := make(scriptObj, len(scrObj))

					for k, v := range scrObj {
						checkCtxErr(c)
						newVal := passByValue(*v.val)
						clone[k] = valHolder{val: &newVal}
					}

					callScope.Variables[param.A] = reflect.ValueOf(clone)
				default:
					panic(fmt.Errorf("unknown script cust type kind %v", custType.Kind))
				}

			}

		}

		_, err := execBlock(callScope, scriptFnc.Body)
		if err != nil {
			panic(err) //TODO: handle this panic
		}

		for i := 0; i < funcType.NumOut(); i++ {
			checkCtxErr(c)
			if !c.ctx.Return[i].IsValid() || (isNillable(c.ctx.Return[i].Type()) && c.ctx.Return[i].IsNil()) {
				ptr := reflect.New(funcType.Out(i))
				c.ctx.Return[i] = ptr.Elem()
			} else {
				if retFunc, ok := c.ctx.Return[i].Interface().(scriptFunc); ok {
					c.ctx.Return[i] = makeFunc(c, funcType.Out(i), retFunc)
				} else {
					coerced, ok := ReflectCoerce(c, c.ctx.Return[i], funcType.Out(i))
					if !ok {
						panic("makefunc: coerce failed")
					}
					c.ctx.Return[i] = coerced
				}
			}
		}

		ret := c.ctx.Return
		c.ctx.Return = nil

		return ret
	})

	return scriptFncWrapper

}

func appendCallArg(c *runScope, argExpr ast.Expr, argType reflect.Type, args []reflect.Value) ([]reflect.Value, error) {
	checkCtxErr(c)
	arg, err := execExpr(c, argExpr, 1, nil)
	if err != nil {
		return nil, err
	}

	targetArg, ok := ReflectCoerce(c, arg[0], argType)
	if !ok {
		return nil, fmt.Errorf("call arg: could not convert %v to %v", arg[0].Type(), argType)
	}

	if fncWrapper, ok := targetArg.Interface().(anyFuncWrapper); ok {
		targetArg = fncWrapper.anyFunc
	}

	targetArg = reflect.ValueOf(passByValue(targetArg.Interface()))

	args = append(args, targetArg)

	return args, nil
}

func appendCallVarArg(c *runScope, argExpr ast.Expr, argType reflect.Type, args reflect.Value) (reflect.Value, error) {
	checkCtxErr(c)
	arg, err := execExpr(c, argExpr, 1, nil)
	if err != nil {
		return reflect.Value{}, err
	}

	if arg[0].Type().ConvertibleTo(argType) {
		arg[0] = arg[0].Convert(argType)
	}

	if arg[0].CanInterface() {
		if scriptFnc, ok := arg[0].Interface().(scriptFunc); ok {

			scriptFncWrapper := reflect.MakeFunc(argType, func(args []reflect.Value) (results []reflect.Value) {

				c.ctx.Return = nil

				callScope := &runScope{
					ctx:       c.ctx,
					parent:    nil, //I don't think this function inherits the parents variables
					Variables: make(map[string]reflect.Value, 10),
				}

				for argNum, arg := range args {
					checkCtxErr(c)
					//arg = arg.Convert()
					param := scriptFnc.Type.Params.List[argNum]
					//TODO: this is wrong need scopes
					//TODO: what about tuple returns...

					cloneArg := reflect.ValueOf(passByValue(arg.Interface()))

					callScope.Variables[param.Names[0].Name] = cloneArg

				}

				_, err := execBlock(callScope, scriptFnc.Body)
				if err != nil {
					panic(err) //TODO: handle this panic
				}
				return c.ctx.Return
			})

			args = reflect.Append(args, scriptFncWrapper)
		} else {
			var argVal reflect.Value
			if isNil(arg[0]) {
				argVal = reflect.Zero(argType)
			} else {
				argVal = reflect.ValueOf(passByValue(arg[0].Convert(argType).Interface()))
			}

			args = reflect.Append(args, argVal)
		}

	} else {

		var argVal reflect.Value
		if isNil(arg[0]) {
			argVal = reflect.Zero(argType)
		} else {
			argVal = reflect.ValueOf(passByValue(arg[0].Convert(argType).Interface()))
		}

		args = reflect.Append(args, argVal)
	}

	return args, nil

}

func transform(v reflect.Value, targetType reflect.Type) reflect.Value {

	for _, t := range transformers {
		v = t(v, targetType)
	}

	return v
}

func typeErase(v reflect.Value) reflect.Value {

	for _, t := range erasers {
		v = t(v)
	}

	return v
}

type Number interface {
	constraints.Integer | constraints.Float
}

func handleBinaryNumericExpr[T Number](t *ast.BinaryExpr, left []reflect.Value, right []reflect.Value) ([]reflect.Value, error) {

	switch t.Op.String() {
	case "==":
		return []reflect.Value{reflect.ValueOf(left[0].Equal(right[0]))}, nil
	case "!=":
		return []reflect.Value{reflect.ValueOf(!left[0].Equal(right[0]))}, nil
	case "+":
		lVal := left[0].Convert(reflect.TypeFor[T]()).Interface().(T)
		rVal := right[0].Convert(reflect.TypeFor[T]()).Interface().(T)

		return []reflect.Value{reflect.ValueOf(lVal + rVal)}, nil
	case "-":
		lVal := left[0].Convert(reflect.TypeFor[T]()).Interface().(T)
		rVal := right[0].Convert(reflect.TypeFor[T]()).Interface().(T)

		return []reflect.Value{reflect.ValueOf(lVal - rVal)}, nil
	case "*":
		lVal := left[0].Convert(reflect.TypeFor[T]()).Interface().(T)
		rVal := right[0].Convert(reflect.TypeFor[T]()).Interface().(T)

		return []reflect.Value{reflect.ValueOf(lVal * rVal)}, nil
	case "/":
		lVal := left[0].Convert(reflect.TypeFor[T]()).Interface().(T)
		rVal := right[0].Convert(reflect.TypeFor[T]()).Interface().(T)

		return []reflect.Value{reflect.ValueOf(lVal / rVal)}, nil
	case "%":
		lVal := left[0].Convert(reflect.TypeFor[T]()).Interface().(T)
		rVal := right[0].Convert(reflect.TypeFor[T]()).Interface().(T)

		return []reflect.Value{reflect.ValueOf(int(lVal) % int(rVal))}, nil
	default:
		return nil, fmt.Errorf("unknown numeric binary expr %v", t.Op)
	}
}

func execExpr(c *runScope, expr ast.Expr, retCount int, lhs reflect.Type) ([]reflect.Value, error) {
	checkCtxErr(c)
	switch t := expr.(type) {
	case *ast.BasicLit:
		switch t.Kind {
		case token.STRING:
			return []reflect.Value{reflect.ValueOf(t.Value[1 : len(t.Value)-1])}, nil
		case token.INT:
			i, err := strconv.ParseInt(t.Value, 0, 32)
			if err != nil {
				return nil, err
			}
			if lhs != nil {
				return []reflect.Value{reflect.ValueOf(i).Convert(lhs)}, nil
			} else {
				return []reflect.Value{reflect.ValueOf(int(i))}, nil
			}
		case token.FLOAT:
			i, err := strconv.ParseFloat(t.Value, 64)
			if err != nil {
				return nil, err
			}
			if lhs != nil {
				return []reflect.Value{reflect.ValueOf(i).Convert(lhs)}, nil
			} else {
				return []reflect.Value{reflect.ValueOf(i)}, nil
			}
		default:
			return nil, fmt.Errorf("unknown BasicLit Kind %v", t.Kind)
		}
	case *ast.CallExpr:
		fncExpr := t.Fun

		name, ok := fncExpr.(*ast.Ident)
		//Builtins
		if ok {
			switch name.Name {
			case "append":
				sliceRet, err := execExpr(c, t.Args[0], 1, nil)
				if err != nil {
					return nil, err
				}

				slice := sliceRet[0]
				if !slice.IsValid() || slice.IsNil() {
					slice = reflect.MakeSlice(slice.Type(), 0, 0)
				}

				if t.Ellipsis != token.NoPos {
					val, err := execExpr(c, t.Args[1], 1, nil)
					if err != nil {
						return nil, err
					}

					cSlice, ok := ReflectCoerce(c, val[0], slice.Type())
					if !ok {
						return nil, fmt.Errorf("append: could not convert %v to %v", val[0].Type(), slice.Type())
					}

					slice = reflect.AppendSlice(slice, cSlice)

				} else {

					for i := 1; i < len(t.Args); i++ {
						checkCtxErr(c)
						val, err := execExpr(c, t.Args[i], 1, nil)
						if err != nil {
							return nil, err
						}
						cval, ok := ReflectCoerce(c, val[0], slice.Type().Elem())
						if !ok {
							return nil, fmt.Errorf("append: could not convert %v to %v", val[0].Type(), slice.Type().Elem())
						}
						slice = reflect.Append(slice, cval)
					}
				}

				return []reflect.Value{slice}, nil

			case "len":
				argExpr := t.Args[0]

				arg, err := execExpr(c, argExpr, 1, nil)
				if err != nil {
					return nil, err
				}

				ret := arg[0].Len()

				return []reflect.Value{reflect.ValueOf(ret)}, nil
			}
		}

		fncRet, err := execExpr(c, fncExpr, 1, nil)
		if err != nil {
			return nil, err
		}

		fnc := fncRet[0]

		if fnc.Kind() == reflect.Interface {
			fnc = fnc.Elem()
		}

		if fnc.Kind() == reflect.Func {
			if gFnc, ok := fnc.Interface().(GenericFunction); ok {
				tParams, err := getTypeParams(t.Fun)
				if err != nil {
					return nil, fmt.Errorf("generic function %v : %w", name, err)
				}

				anyFnc := gFnc(tParams)

				var args []reflect.Value
				for argNum, argExpr := range t.Args {
					checkCtxErr(c)
					argType := anyFnc.Type().In(argNum)
					args, err = appendCallArg(c, argExpr, argType, args)
					if err != nil {
						return nil, err
					}
				}
				ret := anyFnc.Call(args)

				return ret, nil

			} else if fnc.Type().IsVariadic() {
				var args []reflect.Value
				numArgs := fnc.Type().NumIn()
				for argNum := 0; argNum < numArgs-2; argNum++ {
					checkCtxErr(c)
					argExpr := t.Args[argNum]
					origArgType := fnc.Type().In(argNum)
					args, err = appendCallArg(c, argExpr, origArgType, args)
					if err != nil {
						return nil, err
					}
				}

				variadicType := fnc.Type().In(numArgs - 1)
				varArgs := reflect.MakeSlice(variadicType, 0, len(t.Args)-(numArgs-1))
				for argNum := numArgs - 1; argNum < len(t.Args); argNum++ {
					checkCtxErr(c)
					argExpr := t.Args[argNum]

					varArgs, err = appendCallVarArg(c, argExpr, variadicType.Elem(), varArgs)
					if err != nil {
						return nil, err
					}
				}
				args = append(args, varArgs)

				ret := fnc.CallSlice(args)

				return ret, nil
			} else {

				var args []reflect.Value
				for argNum, argExpr := range t.Args {
					checkCtxErr(c)
					argType := fnc.Type().In(argNum)
					args, err = appendCallArg(c, argExpr, argType, args)
					if err != nil {
						return nil, err
					}
				}
				ret := fnc.Call(args)

				return ret, nil
			}

		} else {

			iface := fnc.Interface()
			switch fncT := iface.(type) {
			case scriptFunc:
				callScope := &runScope{
					ctx:       fncT.Scope.ctx,
					Variables: make(map[string]reflect.Value, 10),
					parent:    fncT.Scope,
				}

				i := 0
				for _, argExpr := range t.Args {
					checkCtxErr(c)

					arg, err := execExpr(c, argExpr, 1, nil)
					if err != nil {
						return nil, err
					}

					//Pass by value
					cloneArg := reflect.New(arg[0].Type())
					cloneArg.Elem().Set(arg[0])

					param := fncT.Type.Params.List[i]

					callScope.Variables[param.Names[0].Name] = cloneArg.Elem()
					i++
				}

				_, err := execBlock(callScope, fncT.Body)
				if err != nil {
					return nil, err
				}

				for _, deferExpr := range c.DeferList {
					checkCtxErr(c)
					_, err := execExpr(c, deferExpr, 1, nil)
					if err != nil {
						return nil, err
					}
				}

				return c.ctx.Return, nil
			case anyFuncWrapper:
				if gFnc, ok := fncT.origFunc.Interface().(GenericFunction); ok {
					tParams, err := getTypeParams(t.Fun)
					if err != nil {
						return nil, fmt.Errorf("generic function %v : %w", name, err)
					}

					anyFnc := gFnc(tParams)

					var args []reflect.Value
					for argNum, argExpr := range t.Args {
						checkCtxErr(c)
						argType := fncT.origFunc.Type().In(argNum)
						args, err = appendCallArg(c, argExpr, argType, args)
						if err != nil {
							return nil, err
						}
					}
					ret := anyFnc.Call(args)

					return ret, nil

				} else {

					if fncT.anyFunc.Type().IsVariadic() {
						var args []reflect.Value
						numArgs := fncT.anyFunc.Type().NumIn()
						for argNum := 0; argNum < numArgs-2; argNum++ {
							checkCtxErr(c)
							argExpr := t.Args[argNum]
							origArgType := fncT.origFunc.Type().In(argNum)
							args, err = appendCallArg(c, argExpr, origArgType, args)
							if err != nil {
								return nil, err
							}
						}

						variadicType := fncT.anyFunc.Type().In(numArgs - 1)
						varArgs := reflect.MakeSlice(variadicType, 0, len(t.Args)-(numArgs-1))
						for argNum := numArgs - 1; argNum < len(t.Args); argNum++ {
							checkCtxErr(c)
							argExpr := t.Args[argNum]

							varArgs, err = appendCallVarArg(c, argExpr, variadicType.Elem(), varArgs)
							if err != nil {
								return nil, err
							}
						}
						args = append(args, varArgs)

						return fncT.anyFunc.CallSlice(args), nil

					} else {

						var args []reflect.Value
						for argNum, argExpr := range t.Args {
							checkCtxErr(c)
							argType := fncT.origFunc.Type().In(argNum)
							args, err = appendCallArg(c, argExpr, argType, args)
							if err != nil {
								return nil, err
							}
						}
						return fncT.anyFunc.Call(args), nil
					}
				}
			default:
				return nil, fmt.Errorf("unknown func type %T", fncT)
			}
		}

	case *ast.FuncLit:
		return []reflect.Value{reflect.ValueOf(scriptFunc{
			Scope: c,
			Body:  t.Body,
			Type:  t.Type,
		})}, nil
	case *ast.CompositeLit:
		goType, custType, err := resolveType(c, t.Type)
		if err != nil {
			return nil, err
		}

		if goType == nil {
			//Script Custom Type

			switch custType.Kind {
			case reflect.Struct:
				ret := make(scriptObj, len(t.Elts))
				for _, elt := range t.Elts {
					checkCtxErr(c)
					kv := elt.(*ast.KeyValueExpr)
					val, err := execExpr(c, kv.Value, 1, nil)
					if err != nil {
						return nil, err
					}
					scrVal := val[0].Interface()
					ret[kv.Key.(*ast.Ident).Name] = valHolder{
						val: &scrVal,
					}
				}
				return []reflect.Value{reflect.ValueOf(ret)}, nil
			case reflect.Slice:
				ret := make([]any, 0, len(t.Elts))
				for _, elt := range t.Elts {
					checkCtxErr(c)
					val, err := execExpr(c, elt, 1, nil)
					if err != nil {
						return nil, err
					}
					ret = append(ret, val[0].Interface())
				}
				return []reflect.Value{reflect.ValueOf(ret)}, nil
			case reflect.Map:
				ret := make(scriptMap, len(t.Elts))
				for _, elt := range t.Elts {
					checkCtxErr(c)
					kv := elt.(*ast.KeyValueExpr)
					val, err := execExpr(c, kv.Value, 1, nil)
					if err != nil {
						return nil, err
					}
					scrVal := val[0].Interface()
					key, err := execExpr(c, kv.Key, 1, nil)
					if err != nil {
						return nil, err
					}
					ret[key[0].Interface()] = valHolder{
						val: &scrVal,
					}
				}
				return []reflect.Value{reflect.ValueOf(ret)}, nil
			default:
				return nil, fmt.Errorf("unknown composite literal custType.Kind %v", custType.Kind)
			}

		} else {
			switch goType.Kind() {
			case reflect.Slice:
				ret := reflect.MakeSlice(goType, 0, len(t.Elts))
				for _, elt := range t.Elts {
					checkCtxErr(c)
					val, err := execExpr(c, elt, 1, goType.Elem())
					if err != nil {
						return nil, err
					}
					ret = reflect.Append(ret, val[0])
				}
				return []reflect.Value{ret}, nil
			case reflect.Map:
				ret := reflect.MakeMapWithSize(goType, len(t.Elts))
				for _, elt := range t.Elts {
					checkCtxErr(c)
					keyVal := elt.(*ast.KeyValueExpr)
					val, err := execExpr(c, keyVal.Value, 1, nil)
					if err != nil {
						return nil, err
					}
					key, err := execExpr(c, keyVal.Key, 1, nil)
					if err != nil {
						return nil, err
					}
					ret.SetMapIndex(key[0], val[0])
				}
				return []reflect.Value{ret}, nil
			case reflect.Struct:
				ret := reflect.New(goType)
				for _, elt := range t.Elts {
					checkCtxErr(c)
					keyVal := elt.(*ast.KeyValueExpr)
					val, err := execExpr(c, keyVal.Value, 1, nil)
					if err != nil {
						return nil, err
					}

					fieldName := keyVal.Key.(*ast.Ident)
					field, err := resolveField(c, ret, fieldName)
					if err != nil {
						return nil, err
					}

					field.Set(val[0])
				}
				return []reflect.Value{ret.Elem()}, nil

			default:
				return nil, fmt.Errorf("unimplemented litType %v : %v", goType, goType.Kind())
			}
		}
	case *ast.Ident:

		switch t.Name {
		case "nil":
			return []reflect.Value{reflect.ValueOf(nil)}, nil
		}

		if obj, ok := c.Resolve(t.Name); ok {
			if lhs != nil && obj.Type() != lhs {
				if obj, ok := ReflectCoerce(c, obj, lhs); ok {
					return []reflect.Value{obj}, nil
				}
			}
			return []reflect.Value{obj}, nil
		}

		if pkg, ok := c.ctx.Imports[t.Name]; ok {
			return []reflect.Value{reflect.ValueOf(pkg)}, nil
		}

		if lhs != nil {
			val := reflect.New(lhs).Elem()
			c.Variables[t.Name] = val
			return []reflect.Value{val}, nil
		}

		return nil, fmt.Errorf("unknown identifier %v", t.Name)

	case *ast.UnaryExpr:
		switch t.Op {
		case token.NOT:
			val, err := execExpr(c, t.X, 1, nil)
			if err != nil {
				return nil, err
			}

			b := val[0].Bool()

			return []reflect.Value{reflect.ValueOf(!b)}, nil

		case token.AND:

			switch sel := t.X.(type) {
			case *ast.SelectorExpr:
				objRet, err := execExpr(c, sel.X, 1, nil)
				if err != nil {
					return nil, err
				}

				obj := objRet[0]

				fieldName := sel.Sel.Name

				if scrObj, ok := obj.Elem().Interface().(scriptObj); ok {

					valH := scrObj[fieldName]

					return []reflect.Value{
						reflect.ValueOf(valH.val),
					}, nil

				} else {

					val := obj.Elem().FieldByName(fieldName).Addr()

					return []reflect.Value{
						val,
					}, nil
				}
			default:
				val, err := execExpr(c, sel, 1, nil)
				if err != nil {
					return nil, err
				}

				if val[0].CanAddr() {
					return []reflect.Value{val[0].Addr()}, nil
				} else {
					ptr := reflect.New(val[0].Type())
					ptr.Elem().Set(val[0])

					return []reflect.Value{ptr}, nil
				}

			}

		default:
			return nil, fmt.Errorf("unknown unary expr %v", t.Op)
		}
	case *ast.BinaryExpr:
		left, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return nil, err
		}

		right, err := execExpr(c, t.Y, 1, nil)
		if err != nil {
			return nil, err
		}

		switch left[0].Type().Kind() {
		case reflect.Float64:
			return handleBinaryNumericExpr[float64](t, left, right)
		case reflect.Int:
			return handleBinaryNumericExpr[int](t, left, right)
		case reflect.String:
			switch t.Op.String() {
			case "==":
				return []reflect.Value{reflect.ValueOf(left[0].Equal(right[0]))}, nil
			case "!=":
				return []reflect.Value{reflect.ValueOf(!left[0].Equal(right[0]))}, nil
			case "+":
				lVal := left[0].Convert(reflect.TypeFor[string]()).Interface().(string)
				rVal := right[0].Convert(reflect.TypeFor[string]()).Interface().(string)

				return []reflect.Value{reflect.ValueOf(lVal + rVal)}, nil
			default:
				return nil, fmt.Errorf("unknown string binary expr %v", t.Op)
			}
		default:
			switch t.Op.String() {
			case "==":
				return []reflect.Value{reflect.ValueOf(left[0].Equal(right[0]))}, nil
			case "!=":
				return []reflect.Value{reflect.ValueOf(!left[0].Equal(right[0]))}, nil
			default:
				return nil, fmt.Errorf("unknown binary expr kind %v", left[0].Type().Kind())
			}

		}

	case *ast.SelectorExpr:

		obj, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return nil, err
		}

		f, err := resolveField(c, obj[0], t.Sel)
		if err != nil {
			return nil, err
		}

		return []reflect.Value{f}, nil

	case *ast.ParenExpr:
		return execExpr(c, t.X, 1, nil)
	case *ast.IndexExpr:
		v, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return nil, err
		}

		switch v[0].Kind() {
		case reflect.Map:
			ix, err := execExpr(c, t.Index, 1, nil)
			if err != nil {
				return nil, err
			}

			//TODO: what about retcount support for map val,ok :=
			return []reflect.Value{v[0].MapIndex(ix[0])}, nil
		case reflect.Slice, reflect.Array:
			ix, err := execExpr(c, t.Index, 1, nil)
			if err != nil {
				return nil, err
			}

			intIx, ok := ReflectCoerce(c, ix[0], reflect.TypeFor[int]())
			if !ok {
				return nil, fmt.Errorf("index: could not convert %v to int", ix[0].Type())
			}

			return []reflect.Value{v[0].Index(intIx.Interface().(int))}, nil
		case reflect.Func:
			//Generic Type Erasure
			return v, nil

		default:
			return nil, fmt.Errorf("unknown index expr target %v", v[0].Kind())
		}
	case *ast.IndexListExpr:
		elem, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return nil, err
		}
		if gfnc, ok := elem[0].Interface().(GenericFunction); ok {

			var types []reflect.Type
			for _, i := range t.Indices {
				checkCtxErr(c)
				goTyp, _, err := resolveType(c, i)
				if err != nil {
					return nil, err
				}
				if goTyp == nil {
					panic("cust type generic function")
				}
				types = append(types, goTyp)
			}

			gFncInstance := gfnc(types)

			return []reflect.Value{gFncInstance}, nil
		} else {
			return elem, nil
		}
	case *ast.TypeAssertExpr:

		goType, custType, err := resolveType(c, t.Type)
		if err != nil {
			return nil, err
		}

		val, err := execExpr(c, t.X, 1, nil)
		if err != nil {
			return nil, err
		}

		if goType != nil {
			c, ok := ReflectCoerce(c, val[0], goType)
			switch retCount {
			case 1:
				if ok {
					return []reflect.Value{c}, nil
				} else {
					return nil, fmt.Errorf("type Assert failed %T.(%v)", c.Type(), goType)
				}
			case 2:
				return []reflect.Value{c, reflect.ValueOf(ok)}, nil
			default:
				return nil, fmt.Errorf("type Assert expected 1 or 2 return values %v", retCount)
			}
		} else {
			return nil, fmt.Errorf("type Assert failed custType %v", custType)
		}
	default:
		return nil, fmt.Errorf("unknown expr %T", t)
	}
}

func execDecl(c *runScope, decl ast.Decl) error {
	checkCtxErr(c)
	switch t := decl.(type) {
	case *ast.FuncDecl:
		if t.Recv == nil {
			c.Variables[t.Name.Name] = reflect.ValueOf(scriptFunc{
				Scope: c,
				Body:  t.Body,
				Type:  t.Type,
			})
			return nil
		} else {
			//TODO: implement receivers.
			return errors.New("receivers not implemented")
		}
	case *ast.GenDecl:
		switch t.Tok {
		case token.VAR:
			for _, v := range t.Specs {
				checkCtxErr(c)
				vSpec := v.(*ast.ValueSpec)
				for i, name := range vSpec.Names {
					checkCtxErr(c)
					if vSpec.Values != nil {
						val, err := execExpr(c, vSpec.Values[i], 1, nil)
						if err != nil {
							return err
						}

						goType, _, err := resolveType(c, vSpec.Type)
						if err != nil {
							return err
						}

						if goType != nil {
							ptr := reflect.New(goType)

							coerced, ok := ReflectCoerce(c, val[0], goType)
							if !ok {
								return fmt.Errorf("var: could not convert from %v to %v", val[0].Type(), goType)
							}

							ptr.Elem().Set(coerced)

							c.Variables[name.Name] = ptr.Elem()
						} else {
							panic(fmt.Errorf("var cust type"))
						}
					} else {

						goType, _, err := resolveType(c, vSpec.Type)
						if err != nil {
							return err
						}

						if goType == nil {
							val := make(scriptObj)
							c.Variables[name.Name] = reflect.ValueOf(val)
						} else {

							ptr := reflect.New(goType)

							c.Variables[name.Name] = ptr.Elem()
						}

					}
				}
			}
			return nil
		case token.IMPORT:
			for _, v := range t.Specs {
				checkCtxErr(c)
				imp := v.(*ast.ImportSpec)
				pkgName := strings.Trim(imp.Path.Value, "\"")
				pkg, ok := knownPackages[pkgName]
				if !ok {
					return fmt.Errorf("unknown package %v", imp.Path.Value)
				}
				if imp.Name != nil {
					if imp.Name.Name == "." {
						for name, fnc := range pkg.Exported {
							checkCtxErr(c)
							c.Variables[name] = fnc
						}

						for name, typ := range pkg.ExportedTypes {
							checkCtxErr(c)
							c.ctx.DotTypes[name] = typ
						}
					} else {
						c.ctx.Imports[imp.Name.Name] = pkg
					}

				} else {
					if pkg.Package == "" {
						c.ctx.Imports[path.Base(pkg.Name)] = pkg
					} else {
						c.ctx.Imports[path.Base(pkg.Package)] = pkg
					}
				}
			}
			return nil
		case token.TYPE:
			for _, v := range t.Specs {
				checkCtxErr(c)
				typeSpec := v.(*ast.TypeSpec)
				scriptType := scriptCustomType{}
				switch typeType := typeSpec.Type.(type) {
				case *ast.StructType:
					scriptType.Kind = reflect.Struct
					for _, field := range typeType.Fields.List {
						checkCtxErr(c)
						fType, cType, err := resolveType(c, field.Type)
						if err != nil {
							return err
						}

						scriptType.Fields = append(scriptType.Fields, &scriptField{
							Name:       field.Names[0].Name,
							Type:       fType,
							CustomType: cType,
						})
					}
				default:
					return fmt.Errorf("unknown Type Type %T", typeType)
				}
				c.ctx.Types[typeSpec.Name.Name] = &scriptType

			}
			return nil
		default:
			return fmt.Errorf("unknown GenDecl Tok %v", t.Tok)
		}
	default:
		return fmt.Errorf("unknown decl %T", t)
	}

}

func Run(ctx context.Context, script string) error {

	c := runContext{
		Imports:  make(map[string]*Package, 10),
		Globals:  make(map[string]reflect.Value, 10),
		Types:    make(map[string]*scriptCustomType, 10),
		DotTypes: make(map[string]reflect.Type, 10),
		Ctx:      ctx,
	}

	scope := runScope{
		ctx:       &c,
		Variables: make(map[string]reflect.Value, 10),
	}

	fset := token.NewFileSet()

	fileNode, err := parser.ParseFile(fset, "main.go", script, parser.ParseComments)
	if err != nil {
		return err
	}

	//find main func
	var mainFunc *ast.FuncDecl
	for _, decl := range fileNode.Decls {

		if fncDecl, ok := decl.(*ast.FuncDecl); ok {
			if fncDecl.Name.Name == "main" {
				mainFunc = fncDecl
			}
		}

		err := execDecl(&scope, decl)
		if err != nil {
			return err
		}
	}

	if mainFunc == nil {
		return errors.New("main not found")
	}

	_, err = execBlock(&scope, mainFunc.Body)
	return err
}
