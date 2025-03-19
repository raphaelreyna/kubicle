package main

import (
	"fmt"
	"time"
)

func main() {
	for {
		fmt.Printf("[my-service] %v\n", time.Now())
		time.Sleep(1 * time.Second)
	}
}
