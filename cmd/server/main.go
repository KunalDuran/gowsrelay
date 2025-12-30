package main

import (
	"log"
	"net/http"

	"github.com/KunalDuran/gowsrelay/server"
)

func main() {

	http.HandleFunc("/ws", server.HandleWebSocket)
	http.HandleFunc("/tcp", server.HandleTCPProxy)
	http.HandleFunc("/status", server.HandleStatus)
	http.HandleFunc("/health", server.HandleHealth)

	log.Println("Send request to http://localhost:8090/tcp?role=producer&topic=mytopic")
	log.Println("role can be 'producer' or 'subscriber'")

	log.Fatal(http.ListenAndServe(":8090", nil))

}
