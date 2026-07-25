// cmd/monitor-dll/main.go
//go:build windows

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	_ "github.com/sagernet/sing-box/experimental/libbox"
)

func main() {}
