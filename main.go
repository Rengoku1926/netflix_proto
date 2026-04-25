package main

import (
	"fmt"
	"netflix-proto/config"
)

func main() {
	cfg := config.Load()
	fmt.Printf("config loaded — payment fail threshold = %d\n",
        cfg.Breakers.Payment.FailureThreshold)
}