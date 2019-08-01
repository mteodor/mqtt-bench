package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/GaryBoone/GoStats/stats"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type SubTimes map[string][]float64

// Client - represents mqtt client
type Client struct {
	ID         string
	BrokerURL  string
	BrokerUser string
	BrokerPass string
	MsgTopic   string
	MsgSize    int
	MsgCount   int
	MsgQoS     byte
	Quiet      bool
}

// RunPublisher - runs publisher
func (c *Client) RunPublisher(res chan *RunResults) {
	newMsgs := make(chan *Message)
	pubMsgs := make(chan *Message)
	doneGen := make(chan bool)
	donePub := make(chan bool)
	runResults := new(RunResults)

	started := time.Now()
	// start generator
	go c.genMessages(newMsgs, doneGen)
	// start publisher
	go c.pubMessages(newMsgs, pubMsgs, doneGen, donePub)

	times := []float64{}
	for {
		select {
		case m := <-pubMsgs:
			cid := m.ID
			if m.Error {
				log.Printf("CLIENT %v ERROR publishing message: %v: at %v\n", c.ID, m.Topic, m.Sent.Unix())
				runResults.Failures++
			} else {
				log.Printf("Message published: %v: sent: %v delivered: %v flight time: %v\n", m.Topic, m.Sent, m.Delivered, m.Delivered.Sub(m.Sent))
				runResults.Successes++
				runResults.ID = cid
				times = append(times, float64(m.Delivered.Sub(m.Sent).Nanoseconds()/1000)) // in microseconds
			}
		case <-donePub:
			// calculate results
			duration := time.Now().Sub(started)
			runResults.MsgTimeMin = stats.StatsMin(times)
			runResults.MsgTimeMax = stats.StatsMax(times)
			runResults.MsgTimeMean = stats.StatsMean(times)
			runResults.MsgTimeStd = stats.StatsSampleStandardDeviation(times)
			runResults.RunTime = duration.Seconds()
			runResults.MsgsPerSec = float64(runResults.Successes) / duration.Seconds()

			// report results and exit
			res <- runResults
			return
		}
	}
}

// RunSubscriber - runs a subscriber
func (c *Client) RunSubscriber(wg *sync.WaitGroup, subTimes *SubTimes) {
	defer wg.Done()
	// start generator
	// start subscriber

	c.subscribe(subTimes)

}

func (c *Client) genMessages(ch chan *Message, done chan bool) {
	for i := 0; i < c.MsgCount; i++ {
		msgPayload := MessagePayload{Payload: make([]byte, c.MsgSize)}
		ch <- &Message{
			Topic:   c.MsgTopic,
			QoS:     c.MsgQoS,
			Payload: msgPayload,
		}
	}
	done <- true
	// log.Printf("CLIENT %v is done generating messages\n", c.ID)
	return
}

func (c *Client) subscribe(subTimes *SubTimes) {

	clientID := fmt.Sprintf("sub-%v-%v", time.Now().Format(time.RFC3339Nano), c.ID)
	c.ID = clientID

	onConnected := func(client mqtt.Client) {
		if !c.Quiet {
			log.Printf("CLIENT %v is connected to the broker %v\n", clientID, c.BrokerURL)
		}
	}

	opts := mqtt.NewClientOptions().
		AddBroker(c.BrokerURL).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetOnConnectHandler(onConnected).
		SetConnectionLostHandler(func(client mqtt.Client, reason error) {
			log.Printf("CLIENT %v lost connection to the broker: %v. Will reconnect...\n", clientID, reason.Error())
		})
	if c.BrokerUser != "" && c.BrokerPass != "" {
		opts.SetUsername(c.BrokerUser)
		opts.SetPassword(c.BrokerPass)
	}
	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()

	if token.Error() != nil {
		log.Printf("CLIENT %v had error connecting to the broker: %v\n", clientID, token.Error())
	}

	token = client.Subscribe(c.MsgTopic, 0, func(cl mqtt.Client, msg mqtt.Message) {

		mp := MessagePayload{}
		err := json.Unmarshal(msg.Payload(), &mp)
		if err != nil {
			log.Printf("CLIENT %s failed to decode message", clientID)
		}
		log.Printf("CLIENT %s received message on topic: %v - %s\n", clientID, msg.Topic(), mp.ID)
		// if (*subTimes)[mp.ID] == nil {
		// 	(*subTimes)[mp.ID] = []float64{}
		// }
		// (*subTimes)[mp.ID] = append((*subTimes)[mp.ID], float64(time.Now().Sub(mp.Sent).Nanoseconds()/1000))
	})

	token.Wait()
	fmt.Println("exit subscribing")

}

func (c *Client) pubMessages(in, out chan *Message, doneGen chan bool, donePub chan bool) {

	clientID := fmt.Sprintf("pub-%v-%v", time.Now().Format(time.RFC3339Nano), c.ID)
	c.ID = clientID
	onConnected := func(client mqtt.Client) {
		if !c.Quiet {
			log.Printf("CLIENT %v is connected to the broker %v\n", clientID, c.BrokerURL)
		}
		ctr := 0
		for {
			select {
			case m := <-in:
				m.Sent = time.Now()
				m.ID = clientID
				m.Payload.ID = clientID
				m.Payload.Sent = m.Sent

				pload, _ := json.Marshal(m.Payload)
				token := client.Publish(m.Topic, m.QoS, false, pload)
				token.Wait()
				if token.Error() != nil {
					log.Printf("CLIENT %v Error sending message: %v\n", clientID, token.Error())
					m.Error = true
				} else {
					m.Delivered = time.Now()
					m.Error = false
				}
				out <- m

				if ctr > 0 && ctr%100 == 0 {
					if !c.Quiet {
						log.Printf("CLIENT %v published %v messages and keeps publishing...\n", clientID, ctr)
					}
				}
				ctr++
			case <-doneGen:
				donePub <- true
				if !c.Quiet {
					log.Printf("CLIENT %v is done publishing\n", clientID)
				}
				return
			}
		}
	}

	opts := mqtt.NewClientOptions().
		AddBroker(c.BrokerURL).
		//SetClientID(fmt.Sprintf("mqtt-benchmark-%v-%v", time.Now().Format(time.RFC3339Nano), c.ID)).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetOnConnectHandler(onConnected).
		SetConnectionLostHandler(func(client mqtt.Client, reason error) {
			log.Printf("CLIENT %v lost connection to the broker: %v. Will reconnect...\n", clientID, reason.Error())
		})
	if c.BrokerUser != "" && c.BrokerPass != "" {
		opts.SetUsername(c.BrokerUser)
		opts.SetPassword(c.BrokerPass)
	}
	client := mqtt.NewClient(opts)
	token := client.Connect()
	token.Wait()

	if token.Error() != nil {
		log.Printf("CLIENT %v had error connecting to the broker: %v\n", c.ID, token.Error())
	}
}
