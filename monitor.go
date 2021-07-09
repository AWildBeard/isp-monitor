package main

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"
)

type packetUpdate struct {
	dropped   bool
	seqNumber int
	rttStart  time.Time
	rttStop   time.Time
}

var (
	icmpInterval        time.Duration
	statisticResolution time.Duration
	listenAddress       string
	printVersion        bool

	buildType    string
	buildVersion string
)

func init() {
	flag.DurationVarP(&icmpInterval, "interval", "i", 1*time.Second,
		"set the interval that this tool sends icmp packets on.")
	flag.DurationVarP(&statisticResolution, "resolution", "r", 30*time.Second,
		"set how often this tool generates statistics for observation")
	flag.StringVarP(&listenAddress, "listen", "l", "127.0.0.1:9321",
		"set the listen address for the prometheus metrics server")
	flag.BoolVarP(&printVersion, "version", "v", false,
		"print version information and exit")
}

func main() {
	flag.Parse()

	if printVersion {
		fmt.Printf("%s-%s-%s\n", buildVersion, buildType, runtime.Version())
		os.Exit(1)
	}
	droppedPackets := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "dropped_packets",
		Help:        "Number of dropped packets",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	totalPackets := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "total_packets",
		Help:        "Number of transmitted packets",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	packetLossPercentage := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "loss_percentage",
		Help:        "0.0 - 100.0 percentage of dropped packets",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	roundTripMin := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "rtt_min_msec",
		Help:        "Minimum observed round trip time",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	roundTripAvg := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "rtt_avg_msec",
		Help:        "Average observed round trip time",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	roundTripMax := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "networkloss",
		Name:        "rtt_max_msec",
		Help:        "Maximum observed round trip time",
		ConstLabels: prometheus.Labels{"monitor_target": "1.0.0.1"},
	})

	prometheus.MustRegister(droppedPackets)
	prometheus.MustRegister(totalPackets)
	prometheus.MustRegister(packetLossPercentage)
	prometheus.MustRegister(roundTripMin)
	prometheus.MustRegister(roundTripAvg)
	prometheus.MustRegister(roundTripMax)

	http.Handle("/metrics", promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	))

	fmt.Println("starting http server")
	go func() {
		fmt.Printf("%v\n", http.ListenAndServe("127.0.0.1:9321", nil))
		os.Exit(1)
	}()
	fmt.Println("started http server")

	updateChan := make(chan packetUpdate, 100)
	go func(updateChan chan packetUpdate) {
		droppedPacketsCounter := 0
		totalPacketsCounter := 0
		rttMin := time.Duration((1 << 63) - 1)
		rttAvg := int64(0)
		rttAvgCnt := int64(1)
		rttMax := time.Duration(0)
		ticker := time.NewTicker(statisticResolution)

		for {
			select {
			case update := <-updateChan:
				totalPacketsCounter++
				if !update.dropped {
					rtt := update.rttStop.Sub(update.rttStart)
					if rtt < rttMin {
						rttMin = rtt
					}
					if rtt > rttMax {
						rttMax = rtt
					}
					if rttAvg == 0 {
						rttAvg = int64(rtt)
					} else {
						rttInt := int64(rtt)
						rttAvg = ((rttAvg * rttAvgCnt) + rttInt) / (rttAvgCnt + 1)
						rttAvgCnt++
					}
				} else {
					droppedPacketsCounter++
				}
			case <-ticker.C:
				rttMinFl := float64(0)
				rttAvgFl := float64(0)
				rttMaxFl := float64(0)

				rttMinFl = float64(rttMin/time.Microsecond) / 1000
				rttMinFl += float64(rttMin/time.Millisecond) / 1000

				rttAvgDuration := time.Duration(rttAvg)
				rttAvgFl = float64(rttAvgDuration/time.Microsecond) / 1000
				rttAvgFl += float64(rttAvgDuration/time.Millisecond) / 1000

				rttMaxFl = float64(rttMax/time.Microsecond) / 1000
				rttMaxFl += float64(rttMax/time.Millisecond) / 1000

				fmt.Printf("dropped_packets=%d total_packets=%d loss=%3.2f rtt_min_msec=%4.3f rtt_avg_msec=%4.3f "+
					"rtt_max_msec=%4.3f\n",
					droppedPacketsCounter, totalPacketsCounter, float64(droppedPacketsCounter)/float64(totalPacketsCounter),
					rttMinFl, rttAvgFl, rttMaxFl)

				// Set prometheus metrics
				droppedPackets.Set(float64(droppedPacketsCounter))
				totalPackets.Set(float64(totalPacketsCounter))
				packetLossPercentage.Set(float64(droppedPacketsCounter) / float64(totalPacketsCounter))
				roundTripMin.Set(rttMinFl)
				roundTripAvg.Set(rttAvgFl)
				roundTripMax.Set(rttMaxFl)

				// Reset counters
				droppedPacketsCounter = 0
				totalPacketsCounter = 0
				rttMin = time.Duration((1 << 63) - 1)
				rttAvg = 0
				rttAvgCnt = 1
				rttMax = time.Duration(0)
			}
		}
	}(updateChan)

	icmpBody := &icmp.Echo{Seq: 0}
	icmpMessage := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: icmpBody,
	}

	pingHost := &net.UDPAddr{IP: net.ParseIP("1.0.0.1")}
	ticker := time.NewTicker(icmpInterval)
	buf := make([]byte, 1500)

	fmt.Println("opening icmp listener")
	if con, err := icmp.ListenPacket("udp4", "0.0.0.0"); err == nil {
		fmt.Println("starting icmp request loop")
	pingLoop:
		for {
			time.Sleep(icmpInterval)
			if icmpBody.Seq+1 == 65536 {
				icmpBody.Seq = 0
			} else {
				icmpBody.Seq++
			}

			ticker.Reset(icmpInterval)
			var (
				rttStart time.Time
				rttStop  time.Time
			)

			if icmpReq, err := icmpMessage.Marshal(nil); err == nil {
				err = con.SetWriteDeadline(time.Now().Add(icmpInterval))
				if err != nil {
					fmt.Printf("fatal error setting write deadling on ping channel: %v. PROGRAM EXIT\n", err)
					os.Exit(1)
				}

				if _, err := con.WriteTo(icmpReq, pingHost); err != nil {
					fmt.Printf("failed to write to host %v: %v\n", pingHost, err)
					continue pingLoop
				}
			} else {
				fmt.Printf("fatal error marshaling icmp request: %v. PROGRAM EXIT\n", err)
				os.Exit(1)
			}
			rttStart = time.Now()

		readLoop:
			for {
				err = con.SetReadDeadline(time.Now().Add(icmpInterval))
				if err != nil {
					fmt.Printf("fatal error setting read deadling on ping channel: %v. PROGRAM EXIT\n", err)
					os.Exit(1)
				}

				if numBytesReceived, addr, err := con.ReadFrom(buf); err == nil {
					receivedMessage, receivedMessageError := icmp.ParseMessage(1, buf)
					rxSeqNum := (int(buf[6]) << 8) | int(buf[7]) // recover the sequence number since icmp doesn't provide access
					flushBuf(buf, numBytesReceived)
					if receivedMessageError == nil &&
						receivedMessage.Type == ipv4.ICMPTypeEchoReply &&
						addr.String() == pingHost.String() &&
						icmpBody.Seq == rxSeqNum {
						break readLoop
					}
				}

				select {
				case <-ticker.C:
					updateChan <- packetUpdate{
						dropped:   true,
						seqNumber: icmpBody.Seq,
					}
					continue pingLoop // Time limit for entire ping operation exceeded
				default:
					continue readLoop
				}
			}
			rttStop = time.Now()

			updateChan <- packetUpdate{
				dropped:   false,
				seqNumber: icmpBody.Seq,
				rttStart:  rttStart,
				rttStop:   rttStop,
			}
		}
	} else {
		fmt.Printf("fatal error binding to port: %v. PROGRAM EXIT\n", err)
		os.Exit(1)
	}
}

func flushBuf(buf []byte, len int) {
	for pos := 0; pos < len; pos++ {
		buf[pos] = 0
	}
}
