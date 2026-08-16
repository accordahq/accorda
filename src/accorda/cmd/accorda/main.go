package main

import (
	"fmt"
	"os"

	"accorda/internal/hello"
)

func main() {
	name := "world"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	fmt.Println(hello.Greet(name))
}
