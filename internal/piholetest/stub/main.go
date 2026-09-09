// Command stub serves a fake Pi-hole API. The image system test in CI runs it
// on the runner so the containerised exporter has something to scrape.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()

	log.Printf("stub Pi-hole listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, piholetest.Handler(log.Printf)))
}
