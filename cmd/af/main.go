package main

import (
	"os"

	"github.com/stempeck/agentfactory/internal/cmd"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

var sourceRoot string

func main() {
	mcpstore.SetSourceRoot(sourceRoot)
	mcpstore.SetEnvSourceRoot(os.Getenv("AF_SOURCE_ROOT"))
	cmd.SetSourceRoot(sourceRoot)
	os.Exit(cmd.Execute())
}
