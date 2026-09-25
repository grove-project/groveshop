package main

import (
	"github.com/grove-project/groveshop/runtimeapp"
	groveruntime "github.com/grove-project/grove/runtime"
)

func main() {
	groveruntime.Main(runtimeapp.RuntimeDefinition())
}
