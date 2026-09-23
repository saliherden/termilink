package main

import (
	"fmt"
	"os"

	"github.com/saliherden/termilink/cmd/termilink/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "termilink:", err)
		os.Exit(1)
	}
}
