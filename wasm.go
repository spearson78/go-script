//go:build js && wasm

package goscript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"
	"time"
)

func RunGoCode(code string, timeoutMillis int) (ret string) {
	defer func() {
		if r := recover(); r != nil {
			ret = fmt.Sprintf("panic: %v\n", r)
		}
	}()

	b := bytes.Buffer{}
	x := bufio.NewWriter(&b)
	Out = x

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Millisecond*time.Duration(timeoutMillis),
	)
	defer cancel()

	err := Run(ctx, code)
	if err != nil {
		fmt.Fprintln(Out, err.Error())
	}

	Out = nil
	x.Flush()

	// reading our temp stdout
	return b.String()
}

func jsonWrapper2[P1, P2 any, R any](f func(P1, P2) R) js.Func {
	jsonFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) != 2 {
			return "Invalid no of arguments passed"
		}
		p1JSON := args[0].String()
		var p1 P1
		err := json.Unmarshal([]byte(p1JSON), &p1)
		if err != nil {
			return fmt.Sprintf("unmarshal: '%v' : %v", p1JSON, err.Error())
		}

		p2JSON := args[1].String()
		var p2 P2
		err = json.Unmarshal([]byte(p2JSON), &p2)
		if err != nil {
			return fmt.Sprintf("unmarshal: '%v' : %v", p1JSON, err.Error())
		}

		r := f(p1, p2)
		ret, err := json.Marshal(r)
		if err != nil {
			return fmt.Sprintf("marshal: %v", err.Error())
		}
		return string(ret)
	})
	return jsonFunc
}

func Main() {
	js.Global().Set("RunGoCode", jsonWrapper2(RunGoCode))
	<-make(chan bool)
}
