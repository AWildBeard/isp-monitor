package main

import (
	"context"
	"errors"
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
	received  bool
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
		Help:        "Number of received packets",
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
		Help:        "0.0 - 100.0 percentage of received packets",
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
				if update.received {
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

				if rttMin == time.Duration((1 << 63) - 1) {
					rttMin = time.Duration(0)
				}

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

	fmt.Println("opening icmp listener")
	con, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		fmt.Printf("fatal error binding to port: %v. PROGRAM EXIT\n", err)
		os.Exit(1)
	}

	pingHost := &net.UDPAddr{IP: net.ParseIP("1.0.0.1")}
	buf := make([]byte, 1500)
	deadlineExceeded := false
	for {
		if deadlineExceeded {
			deadlineExceeded = false
		} else {
			time.Sleep(icmpInterval)
		}

		ctxt, cncl := context.WithTimeout(context.Background(), icmpInterval)
		start := time.Now()

		err = sendPing(con, ctxt, pingHost)
		// This shouldn't happen on a write, as icmp is a connectionless protocol. This means there's something
		// messed up with our `con` :/
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				con, err = icmp.ListenPacket("udp4", "0.0.0.0")
				if err != nil {
					fmt.Printf("fatal error binding to port: %v!\n", err)
					os.Exit(1)
				}
			} else {
				fmt.Printf("Unknown error in sendPing: %v\n", err)
			}
		}

		received, err := receivePing(con, ctxt, buf, pingHost)
		// This can happen in the event the network is down. If this happens, we just log it and trust that `received` will be
		// set to false
		if err != nil {
			if ! errors.Is(err, os.ErrDeadlineExceeded) && errors.Is(err, context.DeadlineExceeded) {
				fmt.Printf("Unknown error in receivePing: %v\n", err)
			}
		}

		updateChan <- packetUpdate{
			received:  received,
			seqNumber: seq,
			rttStart:  start,
			rttStop:   time.Now(),
		}

		cncl()
	}

}

func flushBuf(buf []byte, len int) {
	for pos := 0; pos < len; pos++ {
		buf[pos] = 0
	}
}

var seq = 0
// sendPing sends a single ICMP request by writing to `con`. If the write takes time, the timeout is controlled by `context`.
// Context must have a valid deadline set, else sendPing returns os.ErrNoDeadline.
func sendPing(con *icmp.PacketConn, context context.Context, target *net.UDPAddr) (err error) {
	deadline := time.Time{}
	if newDeadline, ok := context.Deadline(); ok {
		deadline = newDeadline
	} else {
		return os.ErrNoDeadline
	}

	if seq == 65536 {
		seq = 0
	}
	defer func() {seq++}()

	msg := &icmp.Message{
		Type:     ipv4.ICMPTypeEcho,
		Code:     0,
		Body:     &icmp.Echo{Seq: seq},
	}

	req, err := msg.Marshal(nil)
	if err != nil {
		fmt.Printf("fatal error marshaling icmp request: %v!\n", err)
		os.Exit(1)
	}

	err = con.SetWriteDeadline(deadline)
	if err != nil {
		return err
	}

	_, err = con.WriteTo(req, target)
	if err != nil {
		return err
	}

	return
}

func receivePing(con *icmp.PacketConn, context context.Context, rcvBuf []byte, target *net.UDPAddr) (received bool, err error) {
	deadline := time.Time{}
	received = false

	if newDeadline, ok := context.Deadline(); ok {
		deadline = newDeadline
	} else {
		return false, os.ErrNoDeadline
	}

	err = con.SetReadDeadline(deadline)
	if err != nil {
		return false, err
	}

	numBytesReceived, addr, err := con.ReadFrom(rcvBuf)
	if err == nil {
		receivedMessage, receivedMessageError := icmp.ParseMessage(1, rcvBuf)
		rxSeqNum := (int(rcvBuf[6]) << 8) | int(rcvBuf[7]) // recover the sequence number since icmp doesn't provide access
		flushBuf(rcvBuf, numBytesReceived)
		if receivedMessageError == nil &&
			receivedMessage.Type == ipv4.ICMPTypeEchoReply &&
			addr.String() == target.String() &&
			seq-1 == rxSeqNum { // seq-1 because seq is always guaranteed to be 1 higher than rxSeqNum because sendPing increments after send
			return true, nil
		}
	}

	return
}