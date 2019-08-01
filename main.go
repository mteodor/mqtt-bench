package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/GaryBoone/GoStats/stats"
)

// Message describes a message
type MessagePayload struct {
	ID      string
	Sent    time.Time
	Payload interface{}
}
type Message struct {
	ID             string
	Topic          string
	QoS            byte
	Payload        MessagePayload
	Sent           time.Time
	Delivered      time.Time
	DeliveredToSub time.Time
	Error          bool
}

// RunResults describes results of a single client / run
type RunResults struct {
	ID        string `json:"id"`
	Successes int64  `json:"successes"`
	Failures  int64  `json:"failures"`

	RunTime     float64 `json:"run_time"`
	MsgTimeMin  float64 `json:"msg_time_min"`
	MsgTimeMax  float64 `json:"msg_time_max"`
	MsgTimeMean float64 `json:"msg_time_mean"`
	MsgTimeStd  float64 `json:"msg_time_std"`

	MsgDelTimeMin  float64 `json:"msg_del_time_min"`
	MsgDelTimeMax  float64 `json:"msg_del_time_max"`
	MsgDelTimeMean float64 `json:"msg_del_time_mean"`
	MsgDelTimeStd  float64 `json:"msg_del_time_std"`

	MsgsPerSec float64 `json:"msgs_per_sec"`
}

// TotalResults describes results of all clients / runs
type TotalResults struct {
	Ratio             float64 `json:"ratio"`
	Successes         int64   `json:"successes"`
	Failures          int64   `json:"failures"`
	TotalRunTime      float64 `json:"total_run_time"`
	AvgRunTime        float64 `json:"avg_run_time"`
	MsgTimeMin        float64 `json:"msg_time_min"`
	MsgTimeMax        float64 `json:"msg_time_max"`
	MsgDelTimeMin     float64 `json:"msg_del_time_min"`
	MsgDelTimeMax     float64 `json:"msg_del_time_max"`
	MsgTimeMeanAvg    float64 `json:"msg_time_mean_avg"`
	MsgTimeMeanStd    float64 `json:"msg_time_mean_std"`
	MsgDelTimeMeanAvg float64 `json:"msg_del_time_mean_avg"`
	MsgDelTimeMeanStd float64 `json:"msg_del_time_mean_std"`
	TotalMsgsPerSec   float64 `json:"total_msgs_per_sec"`
	AvgMsgsPerSec     float64 `json:"avg_msgs_per_sec"`
}

// JSONResults are used to export results as a JSON document
type JSONResults struct {
	Runs   []*RunResults `json:"runs"`
	Totals *TotalResults `json:"totals"`
}

// Connection represents connection
type Connection struct {
	ChannelID string `json:"ChannelID"`
	ThingID   string `json:"ThingID"`
	ThingKey  string `json:"ThingKey"`
}

func main() {

	var (
		broker = flag.String("broker", "tcp://localhost:1883", "MQTT broker endpoint as scheme://host:port")
		qos    = flag.Int("qos", 1, "QoS for published messages")
		size   = flag.Int("size", 100, "Size of the messages payload (bytes)")
		count  = flag.Int("count", 100, "Number of messages to send per client")
		pubs   = flag.Int("pubs", 20, "Number of clients to start")
		subs   = flag.Int("subs", 1, "Number of clients to start")
		format = flag.String("format", "text", "Output format: text|json")
		quiet  = flag.Bool("quiet", false, "Suppress logs while running")
	)
	var wg sync.WaitGroup
	subTimes := make(SubTimes)

	flag.Parse()
	if *pubs < 1 && *subs < 1 {
		log.Fatal("Invalid arguments")
	}

	// Open connections jsonFile
	jsonFile, err := os.Open("connections.json")
	// if we os.Open returns an error then handle it
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("Successfully Opened connections.json")
	// defer the closing of our jsonFile so that we can parse it later on
	defer jsonFile.Close()

	byteValue, _ := ioutil.ReadAll(jsonFile)

	connections := []Connection{}
	json.Unmarshal([]byte(byteValue), &connections)

	resCh := make(chan *RunResults)
	done := make(chan bool)

	start := time.Now()
	n := len(connections)
	for i := 0; i < *subs; i++ {
		if !*quiet {
			//log.Println("Starting sub client ", i)
		}

		con := connections[i%n]

		c := &Client{
			ID:         strconv.Itoa(i),
			BrokerURL:  *broker,
			BrokerUser: con.ThingID,
			BrokerPass: con.ThingKey,
			MsgTopic:   getTestTopic(con.ChannelID),
			MsgSize:    *size,
			MsgCount:   *count,
			MsgQoS:     byte(*qos),
			Quiet:      *quiet,
		}
		go c.RunSubscriber(&wg, &subTimes, &done)
	}

	for i := 0; i < *pubs; i++ {
		if !*quiet {
			log.Println("Starting pub client ", i)
		}
		con := connections[i%n]
		c := &Client{
			ID:         strconv.Itoa(i),
			BrokerURL:  *broker,
			BrokerUser: con.ThingID,
			BrokerPass: con.ThingKey,
			MsgTopic:   getTestTopic(con.ChannelID),
			MsgSize:    *size,
			MsgCount:   *count,
			MsgQoS:     byte(*qos),
			Quiet:      *quiet,
		}
		go c.RunPublisher(resCh)
	}

	// collect the results
	fmt.Printf("collecting results")
	var results []*RunResults
	if *pubs > 0 {
		results = make([]*RunResults, *pubs)
	}

	for i := 0; i < *pubs; i++ {
		results[i] = <-resCh
	}
	done <- true

	fmt.Println("processing results")
	totalTime := time.Now().Sub(start)
	totals := calculateTotalResults(results, totalTime, &subTimes)
	if totals == nil {
		return
	}
	// print stats
	printResults(results, totals, *format)
}
func calculateTotalResults(results []*RunResults, totalTime time.Duration, subTimes *SubTimes) *TotalResults {
	if results == nil || len(results) < 1 {
		return nil
	}
	totals := new(TotalResults)
	totals.TotalRunTime = totalTime.Seconds()
	var subTimeRunResults RunResults
	msgTimeMeans := make([]float64, len(results))
	msgTimeMeansDelivered := make([]float64, len(results))
	msgsPerSecs := make([]float64, len(results))
	runTimes := make([]float64, len(results))
	bws := make([]float64, len(results))

	totals.MsgTimeMin = results[0].MsgTimeMin
	for i, res := range results {

		times := (*subTimes)[res.ID]
		subTimeRunResults.MsgTimeMin = stats.StatsMin(times)
		subTimeRunResults.MsgTimeMax = stats.StatsMax(times)
		subTimeRunResults.MsgTimeMean = stats.StatsMean(times)
		subTimeRunResults.MsgTimeStd = stats.StatsSampleStandardDeviation(times)

		res.MsgDelTimeMin = subTimeRunResults.MsgTimeMin
		res.MsgDelTimeMax = subTimeRunResults.MsgTimeMax
		res.MsgDelTimeMean = subTimeRunResults.MsgTimeMean
		res.MsgDelTimeStd = subTimeRunResults.MsgTimeStd

		totals.Successes += res.Successes
		totals.Failures += res.Failures
		totals.TotalMsgsPerSec += res.MsgsPerSec

		if res.MsgTimeMin < totals.MsgTimeMin {
			totals.MsgTimeMin = res.MsgTimeMin
		}

		if res.MsgTimeMax > totals.MsgTimeMax {
			totals.MsgTimeMax = res.MsgTimeMax
		}

		if subTimeRunResults.MsgTimeMin < totals.MsgDelTimeMin {
			totals.MsgDelTimeMin = subTimeRunResults.MsgTimeMin
		}

		if subTimeRunResults.MsgTimeMax > totals.MsgDelTimeMax {
			totals.MsgDelTimeMax = subTimeRunResults.MsgTimeMax
		}

		msgTimeMeansDelivered[i] = subTimeRunResults.MsgTimeMean
		msgTimeMeans[i] = res.MsgTimeMean
		msgsPerSecs[i] = res.MsgsPerSec
		runTimes[i] = res.RunTime
		bws[i] = res.MsgsPerSec
	}
	totals.Ratio = float64(totals.Successes) / float64(totals.Successes+totals.Failures)
	totals.AvgMsgsPerSec = stats.StatsMean(msgsPerSecs)
	totals.AvgRunTime = stats.StatsMean(runTimes)
	totals.MsgDelTimeMeanAvg = stats.StatsMean(msgTimeMeansDelivered)
	totals.MsgDelTimeMeanStd = stats.StatsSampleStandardDeviation(msgTimeMeansDelivered)
	totals.MsgTimeMeanAvg = stats.StatsMean(msgTimeMeans)
	totals.MsgTimeMeanStd = stats.StatsSampleStandardDeviation(msgTimeMeans)

	return totals
}

func printResults(results []*RunResults, totals *TotalResults, format string) {
	switch format {
	case "json":
		jr := JSONResults{
			Runs:   results,
			Totals: totals,
		}
		data, _ := json.Marshal(jr)
		var out bytes.Buffer
		json.Indent(&out, data, "", "\t")

		fmt.Println(string(out.Bytes()))
	default:
		for _, res := range results {
			fmt.Printf("======= CLIENT %s =======\n", res.ID)
			fmt.Printf("Ratio:               %.3f (%d/%d)\n", float64(res.Successes)/float64(res.Successes+res.Failures), res.Successes, res.Successes+res.Failures)
			fmt.Printf("Runtime (s):         %.3f\n", res.RunTime)
			fmt.Printf("Msg time min (us):   %.3f\n", res.MsgTimeMin)
			fmt.Printf("Msg time max (us):   %.3f\n", res.MsgTimeMax)
			fmt.Printf("Msg time mean (us):  %.3f\n", res.MsgTimeMean)
			fmt.Printf("Msg time std (us):   %.3f\n", res.MsgTimeStd)
			fmt.Printf(" ======\n")
			fmt.Printf("Msg del time max (us):   %.3f\n", res.MsgDelTimeMax)
			fmt.Printf("Msg del time mean (us):  %.3f\n", res.MsgDelTimeMean)
			fmt.Printf("Msg time std (us):   %.3f\n", res.MsgDelTimeStd)

			fmt.Printf("Bandwidth (msg/sec): %.3f\n\n", res.MsgsPerSec)
		}
		fmt.Printf("========= TOTAL (%d) =========\n", len(results))
		fmt.Printf("Total Ratio:                 %.3f (%d/%d)\n", totals.Ratio, totals.Successes, totals.Successes+totals.Failures)
		fmt.Printf("Total Runtime (sec):         %.3f\n", totals.TotalRunTime)
		fmt.Printf("Average Runtime (sec):       %.3f\n", totals.AvgRunTime)
		fmt.Printf("Msg time min (us):           %.3f\n", totals.MsgTimeMin)
		fmt.Printf("Msg time max (us):           %.3f\n", totals.MsgTimeMax)
		fmt.Printf("Msg time mean mean (us):     %.3f\n", totals.MsgTimeMeanAvg)
		fmt.Printf("Msg time mean std (us):      %.3f\n", totals.MsgTimeMeanStd)
		fmt.Printf(" ======\n")
		fmt.Printf("Msg del time min (us):           %.3f\n", totals.MsgDelTimeMin)
		fmt.Printf("Msg del time max (us):           %.3f\n", totals.MsgDelTimeMax)
		fmt.Printf("Msg del time mean mean (us):     %.3f\n", totals.MsgDelTimeMeanAvg)
		fmt.Printf("Msg del time mean std (us):      %.3f\n", totals.MsgDelTimeMeanStd)

		fmt.Printf("Average Bandwidth (msg/sec): %.3f\n", totals.AvgMsgsPerSec)
		fmt.Printf("Total Bandwidth (msg/sec):   %.3f\n", totals.TotalMsgsPerSec)
	}
	return
}

func getTestTopic(channelID string) string {
	return "channels/" + channelID + "/messages/test"
}
