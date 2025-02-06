package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	l, err := net.Listen("tcp", ":9988")
	if err != nil {
		panic(err)
	}
	defer l.Close()

	sf, err := os.Create("sent.log")
	if err != nil {
		panic(err)
	}
	defer sf.Close()

	rf, err := os.Create("received.log")
	if err != nil {
		panic(err)
	}
	defer rf.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				panic(err)
			}
			log.Println("client connect", conn.RemoteAddr())
			pconn, err := net.Dial("tcp", "sc.global.luxor.tech:700")
			if err != nil {
				panic(err)
			}
			log.Println("proxy connect")

			go io.Copy(io.MultiWriter(sf, pconn), conn)
			go io.Copy(io.MultiWriter(rf, conn), pconn)
		}
	}()

	<-ctx.Done()
}
