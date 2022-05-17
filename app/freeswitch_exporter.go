package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/nevian427/go-eventsocket/eventsocket"
	"github.com/spf13/pflag"
)

type gwStatus uint8

const (
	gwDown gwStatus = iota
	gwUp
)

type gateways struct {
	XMLName xml.Name  `xml:"gateways"`
	Gws     []gateway `xml:"gateway"`
}

// Информация о шлюзе
type gateway struct {
	XMLName       xml.Name `xml:"gateway"`
	Name          string   `xml:"name"`
	Proxy         string   `xml:"proxy"`
	PingTimestamp uint64   `xml:"ping"`
	PingRTD       float32  `xml:"pingtime"`
	Status        gwStatus `xml:"status"`
	UptimeUSec    uint64   `xml:"uptime-usec"`
	CallsIn       uint64   `xml:"calls-in"`
	CallsOut      uint64   `xml:"calls-out"`
	FailIn        uint64   `xml:"failed-calls-in"`
	FailOut       uint64   `xml:"failed-calls-out"`
}

func (s *gwStatus) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	default:
		*s = gwDown
	case "down":
		*s = gwDown
	case "up":
		*s = gwUp
	}
	return nil
}

/*
func (s gwStatus) MarshalText() ([]byte, error) {
	var name string
	switch s {
	default:
		name = "down"
	case gwDown:
		name = "down"
	case gwUp:
		name = "up"
	}
	return []byte(name), nil
}
*/

// Получение статуса шлюзов с сервера
func getGwStatus(c *eventsocket.Connection) error {
	ev, err := c.Send("api sofia xmlstatus gateway")
	if err != nil {
		return err
	}

	parser := xml.NewDecoder(bytes.NewBufferString(ev.Body))
	// Заглушка для правильной работы декодера - ему обязательно нужен UTF8
	parser.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	gws := gateways{}

	// Парсим в структуру
	err = parser.Decode(&gws)
	if err != nil {
		return err
	}

	// spew.Dump(gws)

	// Выставление метрик
	for _, i := range gws.Gws {
		metrics.GetOrCreateGauge("freeswitch_gateway_status{gw=\""+i.Name+"\"}", func() float64 {
			return float64(i.Status)
		})
		metrics.GetOrCreateGauge("freeswitch_gateway_ping_delay{gw=\""+i.Name+"\"}", func() float64 {
			return float64(i.PingRTD)
		})
		metrics.GetOrCreateCounter("freeswitch_gateway_ping_timestamp{gw=\"" + i.Name + "\"}").Set(i.PingTimestamp)
		metrics.GetOrCreateCounter("freeswitch_gateway_uptime{gw=\"" + i.Name + "\"}").Set(i.UptimeUSec)
		metrics.GetOrCreateCounter("freeswitch_gateway_calls_in{gw=\"" + i.Name + "\"}").Set(i.CallsIn)
		metrics.GetOrCreateCounter("freeswitch_gateway_calls_out{gw=\"" + i.Name + "\"}").Set(i.CallsOut)
		metrics.GetOrCreateCounter("freeswitch_gateway_fail_in{gw=\"" + i.Name + "\"}").Set(i.FailIn)
		metrics.GetOrCreateCounter("freeswitch_gateway_fail_out{gw=\"" + i.Name + "\"}").Set(i.FailOut)
	}
	return nil
}

func main() {
	// для простоты парсим только флаги, но конечно нужно сделать конфиг
	host := pflag.StringP("address", "a", "localhost:8021", "host w/port connect to")
	passw := pflag.StringP("password", "p", "ClueCon", "connect using password")
	pflag.Parse()

	fmt.Printf("Connect to %s using password\n", *host)
	fsc, err := eventsocket.DialTimeout(*host, *passw, 5*time.Second)
	if err != nil {
		log.Fatalf("Server connection error: %s", err)
	}

	defer fsc.Close()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		// Получаем статусы только по запросу - зачем лишний раз мучать сервер.
		// При большой нагрузке/количестве шлюзов нужно будет сделать крон/кэш
		if err = getGwStatus(fsc); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "Error getting gw status from server: %s", err)
		} else {
			metrics.WritePrometheus(w, true)
		}
	})

	// По-хорошему порт нужно занести в реестр Prometheus (он взят оттуда как свободный) и сделать его параметром.
	log.Fatal(http.ListenAndServe(":9839", nil))
}
