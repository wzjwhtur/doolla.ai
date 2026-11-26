package main

import (
	"doolla.ai/sdk/cli"
	"doolla.ai/sdk/integrations/chromalocal"
)

func main() { cli.MainWithMemory(chromalocal.Open) }
