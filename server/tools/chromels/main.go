package main

import (
	"fmt"
	"os"

	"github.com/esonic/nexbox/plugins/okone"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "dev" {
		os.Setenv("NEXBOX_CHROME_DEV", "1")
	}

	token, err := okone.ReadToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Print(token)
}
