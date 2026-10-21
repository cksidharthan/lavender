package main

import (
	"log"

	"github.com/cksidharthan/lavender/cmd"
)

func main() {
	err := cmd.Start()
	if err != nil {
		log.Fatalf("error running application :%v", err)
	}
}
