package main

import (
	"netlesspkg/cmd"
	_ "netlesspkg/pkg/pm/apt"
	_ "netlesspkg/pkg/pm/yum"
)

func main() {
	cmd.Execute()
}
