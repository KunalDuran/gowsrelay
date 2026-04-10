package main

import (
	"log"
	"net/http"

	"github.com/KunalDuran/gowsrelay/server"
)

func main() {
	server.Configure(server.Config{
		// Directory where hex dumps are written. Must exist before the server starts.
		PersistDir: "./streams",
	})

	http.HandleFunc("/ws", server.HandleWebSocket)
	http.HandleFunc("/tcp", server.HandleTCPProxy)
	http.HandleFunc("/status", server.HandleStatus)
	http.HandleFunc("/health", server.HandleHealth)
	http.HandleFunc("/admin/persist", server.HandleAdminPersist)

	log.Println("WebSocket proxy listening on :8090")
	log.Println("Send request to http://localhost:8090/tcp?role=producer&topic=mytopic")
	log.Println("role can be 'producer' or 'subscriber'")

	log.Println("  Enable persist:  POST   http://localhost:8090/admin/persist?topic=<name>")
	log.Println("  Disable persist: DELETE http://localhost:8090/admin/persist?topic=<name>")

	log.Fatal(http.ListenAndServe(":8090", nil))
}
