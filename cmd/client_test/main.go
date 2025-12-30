package main

import (
	"fmt"
	"net"
)

func main() {
	port := "52885"
	target, err := net.Dial("tcp", fmt.Sprintf("localhost:%s", port))
	if err != nil {
		fmt.Println("failed to connect to target localhost:", port, err)
		return
	}
	defer target.Close()

	buf := make([]byte, 32*1024)
	for {
		n, err := target.Read(buf)
		if err != nil {
			return
		}
		fmt.Println("read:", n)
		fmt.Println(string(buf[:n]))
		fmt.Println(buf[:n])
	}

}
