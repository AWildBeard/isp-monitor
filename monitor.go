package main

import (
	"fmt"
	ping "github.com/prometheus-community/pro-bing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	"net/http"
	"os"
	"runtime"
	"time"
)

type hostUpdate struct {
	host string
	*ping.Statistics
}

var (
	statisticResolution time.Duration
	measurementInterval time.Duration
	listenAddress       string
	printVersion        bool
	monitorTargets      []string
	debug               bool

	buildType    string
	buildVersion string
)

func init() {
	flag.DurationVarP(&statisticResolution, "resolution", "r", 30*time.Second,
		"set how often this tool generates statistics for observation")
	flag.DurationVarP(&measurementInterval, "interval", "i", 5*time.Second,
		"set the interval between ICMP requests")
	flag.StringVarP(&listenAddress, "listen", "l", "127.0.0.1:9321",
		"set the listen address for the prometheus metrics server")
	flag.BoolVarP(&printVersion, "version", "v", false,
		"print version information and exit")
	flag.StringSliceVarP(&monitorTargets, "hosts", "h", []string{},
		"the hosts to monitor with ICMP")
	flag.BoolVarP(&debug, "debug", "d", false,
		"enable debug mode")
}

func main() {
	flag.Parse()

	if printVersion {
		fmt.Printf("%s-%s-%s\n", buildVersion, buildType, runtime.Version())
		os.Exit(1)
	}

	if len(monitorTargets) <= 0 {
		fmt.Printf("You must supply at least 1 monitor target\n")
		os.Exit(1)
	}

	droppedPackets := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "dropped_packets",
		Help:      "Number of dropped packets",
	}, []string{"monitor_target"})

	receivedPackets := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "received_packets",
		Help:      "Number of received packets",
	}, []string{"monitor_target"})

	totalPackets := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "total_packets",
		Help:      "Number of sent packets",
	}, []string{"monitor_target"})

	duplicatePackets := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "duplicate_packets",
		Help:      "Number of duplicate packets received",
	}, []string{"monitor_target"})

	packetLossPercentage := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "loss_percentage",
		Help:      "0.0 - 100.0 percentage of received packets",
	}, []string{"monitor_target"})

	roundTripMin := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "rtt_min",
		Help:      "Minimum observed round trip time",
	}, []string{"monitor_target"})

	roundTripAvg := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "rtt_avg",
		Help:      "Average observed round trip time",
	}, []string{"monitor_target"})

	roundTripMax := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "rtt_max",
		Help:      "Maximum observed round trip time",
	}, []string{"monitor_target"})

	roundTripDev := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "networkloss",
		Name:      "rtt_dev",
		Help:      "Standard deviation in observed round trip time",
	}, []string{"monitor_target"})

	prometheus.MustRegister(droppedPackets)
	prometheus.MustRegister(receivedPackets)
	prometheus.MustRegister(totalPackets)
	prometheus.MustRegister(duplicatePackets)
	prometheus.MustRegister(packetLossPercentage)
	prometheus.MustRegister(roundTripMin)
	prometheus.MustRegister(roundTripAvg)
	prometheus.MustRegister(roundTripMax)
	prometheus.MustRegister(roundTripDev)

	http.Handle("/metrics", promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))

	updateChan := make(chan *hostUpdate, 100)
	go func(updateChan chan *hostUpdate) {
		for {
			select {
			case update := <-updateChan:
				// Set prometheus metrics
				droppedPackets.WithLabelValues(update.host).Set(float64(update.PacketsSent - update.PacketsRecv))
				receivedPackets.WithLabelValues(update.host).Set(float64(update.PacketsRecv))
				totalPackets.WithLabelValues(update.host).Set(float64(update.PacketsSent))
				duplicatePackets.WithLabelValues(update.host).Set(float64(update.PacketsRecvDuplicates))
				packetLossPercentage.WithLabelValues(update.host).Set(update.PacketLoss)
				roundTripMin.WithLabelValues(update.host).Set(float64(update.MinRtt.Milliseconds()))
				roundTripAvg.WithLabelValues(update.host).Set(float64(update.AvgRtt.Milliseconds()))
				roundTripMax.WithLabelValues(update.host).Set(float64(update.MaxRtt.Milliseconds()))
				roundTripDev.WithLabelValues(update.host).Set(float64(update.StdDevRtt.Milliseconds()))

				if debug {
					fmt.Printf("%v\n", *update.Statistics)
				}
			}
		}
	}(updateChan)

	for _, host := range monitorTargets {
		go func(host string, updateChan chan *hostUpdate) {
			for {
				pinger, err := ping.NewPinger(host)
				if err != nil {
					fmt.Printf("failed to resolve name %v: %v\n", host, err)
					os.Exit(1)
				}

				pinger.Interval = measurementInterval
				pinger.Timeout = statisticResolution
				pinger.OnFinish = func(statistics *ping.Statistics) {
					updateChan <- &hostUpdate{
						host:       host,
						Statistics: statistics,
					}
				}

				err = pinger.Run()
				if err != nil {
					fmt.Printf("(this shouldn't happen ever) background context was cancelled for %v: %v\n", host, err)
					os.Exit(1)
				}
			}
		}(host, updateChan)
	}

	fmt.Println("starting http server")
	fmt.Printf("%v\n", http.ListenAndServe("127.0.0.1:9321", nil))
	os.Exit(1)
}
