//go:build !generate

package main

import (
	"fmt"
	"os"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-version" {
		fmt.Fprintln(os.Stdout, "Sidera", C.Version)
		return
	}
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
