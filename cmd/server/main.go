package main

import (
	"log"
	"net/http"

	"github.com/KunalDuran/gowsrelay/server"
)

func main() {
	p := server.NewProxy(server.Config{})

	http.HandleFunc("/ws", p.HandleWebSocket)
	http.HandleFunc("/tcp", p.HandleTCPProxy)
	http.HandleFunc("/status", p.HandleStatus)
	http.HandleFunc("/health", p.HandleHealth)
	log.Println("WebSocket proxy listening on :8090")
	log.Println("Send request to http://localhost:8090/tcp?role=producer&topic=mytopic")
	log.Println("role can be 'producer' or 'subscriber'")

	log.Fatal(http.ListenAndServe(":8090", nil))
}
