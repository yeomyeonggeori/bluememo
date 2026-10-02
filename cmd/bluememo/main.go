// Command bluememo serves one person's memory to an agent over the Model
// Context Protocol, so any agent that speaks it can keep its own context
// without a key, a model or a setup step.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
)

// serveCommand is the word mcp.json tells a client to pass, so the two are
// held to each other by a test rather than by memory.
const serveCommand = "mcp"

func main() {
	if len(os.Args) < 2 || os.Args[1] != serveCommand {
		fmt.Fprintln(os.Stderr, "usage: bluememo "+serveCommand+" [-store <path>]")
		os.Exit(2)
	}

	flags := flag.NewFlagSet(serveCommand, flag.ExitOnError)
	storePath := flags.String("store", defaultStorePath(), "the memory file to serve")
	if errorValue := flags.Parse(os.Args[2:]); errorValue != nil {
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if errorValue := serve(ctx, *storePath, os.Stdin, os.Stdout); errorValue != nil {
		fmt.Fprintln(os.Stderr, "bluememo:", errorValue)
		os.Exit(1)
	}
}

func defaultStorePath() string {
	if named := os.Getenv("BLUEMEMO_STORE"); named != "" {
		return named
	}
	home, errorValue := os.UserHomeDir()
	if errorValue != nil {
		return "bluememo.db"
	}
	return filepath.Join(home, ".bluememo", "me.db")
}
