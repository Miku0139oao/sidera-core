//go:build !generate

package main

import "github.com/Miku0139oao/sidera-core/log"

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
