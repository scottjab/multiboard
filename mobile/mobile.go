package mobile

import (
	"context"

	"fmt"
	"log"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/hajimehoshi/ebiten/mobile"
	"github.com/scottjab/multiboard"
)

const (
	ScreenWidth  = multiboard.ScreenWidth
	ScreenHeight = multiboard.ScreenHeight
)

var (
	running bool
)

func IsRunning() bool {
	return running
}

func Start(scale float64) error {
	running = true
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		log.Fatal(err)
	}

	h := ""
	entries := make(chan *zeroconf.ServiceEntry)
	go func(results <-chan *zeroconf.ServiceEntry) {
		for entry := range results {

			h = fmt.Sprintf("%s:%d", entry.HostName[:len(entry.HostName)-len(".local.")], entry.Port)
			log.Println(h)
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
	mb := multiboard.NewMultiBoard(h)
	go mb.C.Run(context.Background())
	go mb.StateUpatater()
	if err := mobile.Start(mb.Update, ScreenWidth, ScreenHeight, scale, "multiboard"); err != nil {
		return err
	}
	return nil
}

func Update() error {
	return mobile.Update()
}

func UpdateTouchesOnAndroid(action int, id int, x, y int) {
	mobile.UpdateTouchesOnAndroid(action, id, x, y)
}

func UpdateTouchesOnIOS(phase int, ptr int64, x, y int) {
	mobile.UpdateTouchesOnIOS(phase, ptr, x, y)
}
