package main

import (
	pkgErrors "errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/juju/errors"
	"github.com/peer-calls/peer-calls/server"
	"github.com/peer-calls/peer-calls/server/logger"
)

const gitDescribe string = "v0.0.0"

//format name of filelog
const LoggerFileNameFormat string = "2006-01-02T15:04:05"

func configure(loggerFactory server.LoggerFactory, args []string) (net.Listener, *server.StartStopper, error) {
	log := loggerFactory.GetLogger("main")

	flags := flag.NewFlagSet("peer-calls", flag.ExitOnError)

	var configFilename string

	flags.StringVar(&configFilename, "c", "", "Config file to use")

	if err := flags.Parse(args); err != nil {
		return nil, nil, errors.Annotate(err, "parse flags")
	}

	configFiles := []string{}
	if configFilename != "" {
		configFiles = append(configFiles, configFilename)
	}
	c, err := server.ReadConfig(configFiles)
	if err != nil {
		return nil, nil, errors.Annotate(err, "read config")
	}

	log.Printf("Using config: %+v", c)
	newAdapter := server.NewAdapterFactory(loggerFactory, c.Store)
	rooms := server.NewAdapterRoomManager(newAdapter.NewAdapter)
	tracks := server.NewMemoryTracksManager(loggerFactory, c.Network.SFU.JitterBuffer)
	mux := server.NewMux(loggerFactory, c.BaseURL, gitDescribe, c.Network, c.ICEServers, rooms, tracks, c.Prometheus)
	l, err := net.Listen("tcp", net.JoinHostPort(c.BindHost, strconv.Itoa(c.BindPort)))
	if err != nil {
		return nil, nil, errors.Annotate(err, "listen")
	}
	startStopper := server.NewStartStopper(server.ServerParams{
		TLSCertFile: c.TLS.Cert,
		TLSKeyFile:  c.TLS.Key,
	}, mux)
	return l, startStopper, nil
}

func start(args []string) (addr *net.TCPAddr, stop func() error, errChan <-chan error) {
	date := time.Now().Format(LoggerFileNameFormat)
	logFile, err := os.OpenFile("./logs/"+date, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		panic(err)

	}
	mw := io.MultiWriter(os.Stderr, logFile)
	loggerFactory := logger.NewFactoryFromEnv("PEERCALLS_", mw)
	loggerFactory.SetDefaultEnabled([]string{
		// "-sdp",
		// "-ws",
		// "-nack",
		// "-rtp",
		// "-rtcp",
		// "-pion:*:trace",
		// "-pion:*:debug",
		// "-pion:*:info",
		"*",
	})

	log := loggerFactory.GetLogger("main")

	ch := make(chan error, 1)
	l, startStopper, err := configure(loggerFactory, args)
	if err != nil {
		ch <- errors.Annotate(err, "configure")
		close(ch)
		return nil, nil, ch
	}

	addr = l.Addr().(*net.TCPAddr)
	log.Printf("Listening on: %s", addr.String())

	go func() {
		err := startStopper.Start(l)
		if !pkgErrors.Is(errors.Cause(err), http.ErrServerClosed) {
			ch <- errors.Annotate(err, "start server")
		}

		close(ch)
	}()

	return addr, startStopper.Stop, ch
}

func main() {
	_, _, errChan := start(os.Args[1:])
	err := <-errChan
	if err != nil {
		fmt.Println("Error starting server: %w", err)
		os.Exit(1)
	}
}
