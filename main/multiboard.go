package main

import (
	"flag"

	"context"
	"fmt"
	"log"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/scottjab/multiboard"
)

func main() {
	host := flag.String("server", "", "server to connect to")
	flag.Parse()
	if host != nil && *host == "" {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			log.Fatal(err)
		}

		h := ""
		entries := make(chan *zeroconf.ServiceEntry)
		go func(results <-chan *zeroconf.ServiceEntry) {
			for entry := range results {

				h = fmt.Sprintf("%s:%d", entry.HostName[:len(entry.HostName)-len(".local.")], entry.Port)
			}
			log.Println("No more entries.")
		}(entries)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(2))
		defer cancel()
		err = resolver.Browse(ctx, "_workstation._tcp", ".local", entries)
		if err != nil {
			log.Fatalln("Failed to browse:", err.Error())
		}

		<-ctx.Done()
		*host = h
		log.Println(*host)
	}
	log.Println(*host)
	mb := multiboard.NewMultiBoard(*host)
	mb.Run()
}
