package main

import (
	"log"
	"os"

	"flag"
	"os/signal"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/pgoergler/go-asterisk-statsd/asterisk/ami"
	"github.com/pgoergler/go-asterisk-statsd/logging"
	"github.com/pgoergler/go-asterisk-statsd/statsd-ami"

	"github.com/quipo/statsd"
)

var (
	Version   string
  Build string
)

type eventHandler func(*statsd.StatsdClient, *ami.Event, map[string]string)

var stopMutex = new(sync.RWMutex)
var stopValue = false

func main() {

	// logging.Init(logging.Trace, os.Stdout)
	logging.Init(logging.Debug, os.Stdout)
	logging.InitWithSyslog(logging.Info, os.Stdout, "asterisk-monitor")
	logging.InitWithSyslog(logging.Warning, os.Stdout, "asterisk-monitor")
	logging.InitWithSyslog(logging.Error, os.Stdout, "asterisk-monitor")

	asteriskInfo := flag.String("asterisk", "", "asterisk connection info. format: user:password@host:port")
	statsdInfo := flag.String("statsd", "", "statsd connection info. format: host:port/prefix")
	flag.Parse()

	statsdEnabled := true
	regexStatsd, _ := regexp.Compile("^(.+?)(:(.*?))?(/(.*?))?$")
	if !regexStatsd.MatchString(*statsdInfo) {
		logging.Error.Println("statsd disabled")
		statsdEnabled = false
	}

	var statsdclient *statsd.Statsd
	if statsdEnabled {

		matches := regexStatsd.FindAllStringSubmatch(*statsdInfo, -1)
		statsdHost := matches[0][1] + ":" + matches[0][3]
		statsdPrefix := matches[0][5]

		client := statsd.Statsd(statsd.NewStatsdClient(statsdHost, statsdPrefix))
		statsdclient = &client
	} else {
		client := statsd.Statsd(statsd.NoopClient{})
		statsdclient = &client
	}

	err := (*statsdclient).CreateSocket()
	if nil != err {
		logging.Error.Println(err)
		os.Exit(1)
	}

	regexAsterisk, _ := regexp.Compile("^(.*?):(.*?)@(.*?)$")
	if !regexAsterisk.MatchString(*asteriskInfo) {
		logging.Error.Println("could not parse asterisk connection info <" + (*asteriskInfo) + ">")
		return
	}

	matches := regexAsterisk.FindAllStringSubmatch((*asteriskInfo), -1)
	asteriskUsername := matches[0][1]
	asteriskPassword := matches[0][2]
	asteriskAddress := matches[0][3]

	amiClient := ami.New(asteriskAddress, asteriskUsername, asteriskPassword)

	amiClient.RegisterHandler("Newchannel", statsdami.NewHandler(statsdclient, statsdami.EventNewChannelHandler))
	amiClient.RegisterHandler("Newstate", statsdami.NewHandler(statsdclient, statsdami.EventNewStateHandler))
	amiClient.RegisterHandler("SoftHangupRequest", statsdami.NewHandler(statsdclient, statsdami.EventSoftHangupHandler))
	amiClient.RegisterHandler("Hangup", statsdami.NewHandler(statsdclient, statsdami.EventHangupHandler))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)

	go func() {
		logging.Info.Println("Version", Version, "Build:", Build)

		for {
			sig := <-sigChan
			logging.Trace.Printf("received signal: %s\n", sig)
			switch sig {
			case os.Interrupt, syscall.SIGTERM:
				{
					logging.Trace.Printf("stop()")
					stop()
					logging.Trace.Printf("Closing")
					amiClient.Close()
					logging.Trace.Printf("Closed")
				}
			case syscall.SIGUSR1:
				{
					logging.Debug.Println("Pending calls:", statsdami.GetPendingCallsCount())
					logging.Debug.Println("Pending responses:", amiClient.GetPendingActionsCount())
					logging.Debug.Println("Gauges:", statsdami.GetGaugeCount())
				}
			case syscall.SIGUSR2:
				{
					file, err := os.OpenFile("dump.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
					if err != nil {
						log.Fatalln("Failed to open dump file:", err)
					}
					logging.Init(logging.Dump, file)
					logging.Dump.Println("-----------")
					ami.Dump(amiClient, logging.Dump)
					statsdami.Dump(logging.Dump)
					statsdami.DumpGauges(logging.Dump)
					file.Close()
				}
			}
		}
	}()

	for !shouldStop() {
		amiClient.StopKeepAlive()

		if err := amiClient.Connect(map[string]string{"Events": "call,command"}); err != nil {
			logging.Error.Println(err)
		} else {
			logging.Info.Println("Connected to", asteriskAddress)
			amiClient.KeepAlive(time.Second * 1)

			amiClient.Run()
			logging.Info.Println("Connection lost")
			amiClient.StopKeepAlive()
		}
		// reconnect after 100ms
		<-time.After(time.Millisecond * 100)
	}
	logging.Info.Println("stopped")
}

func shouldStop() bool {
	stopMutex.Lock()
	defer stopMutex.Unlock()

	return stopValue
}

func stop() {
	stopMutex.Lock()
	defer stopMutex.Unlock()

	stopValue = true
}
