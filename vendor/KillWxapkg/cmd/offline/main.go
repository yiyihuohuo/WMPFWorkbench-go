// Offline-only entry point. Does not import internal/hook or embed its helper executable.
package main

import (
	"errors"
	"flag"
	"github.com/Ackites/KillWxapkg/cmd"
	"io"
	"log"
	"os"
	"regexp"
)

type options struct {
	appid, input, output, extension string
	restore, pretty, noClean, save  bool
}

func parseOptions(args []string) (options, error) {
	var o options
	flags := flag.NewFlagSet("wxapkg-offline", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&o.appid, "id", "", "AppID")
	flags.StringVar(&o.input, "in", "", "input copies")
	flags.StringVar(&o.output, "out", "", "output directory")
	flags.StringVar(&o.extension, "ext", ".wxapkg", "package extension")
	flags.BoolVar(&o.restore, "restore", false, "restore project")
	flags.BoolVar(&o.pretty, "pretty", false, "format code")
	flags.BoolVar(&o.noClean, "noClean", false, "keep intermediates")
	flags.BoolVar(&o.save, "save", false, "save plaintext package")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	if !regexp.MustCompile(`^wx[0-9a-f]{16}$`).MatchString(o.appid) || o.input == "" || flags.NArg() != 0 {
		return o, errors.New("valid -id and -in are required")
	}
	return o, nil
}
func main() {
	o, err := parseOptions(os.Args[1:])
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	cmd.Execute(o.appid, o.input, o.output, o.extension, o.restore, o.pretty, o.noClean, o.save, false)
}
