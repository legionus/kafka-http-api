/*
* Copyright (C) 2015 Alexey Gladkov <gladkov.alexey@gmail.com>
*
* This file is covered by the GNU General Public License,
* which should be included with kafka-http-api as the file COPYING.
*/

package main

import (
	uuid "github.com/satori/go.uuid"

	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type kafkaSender struct {
	Project string `json:"project"`
	Cluster string `json:"cluster"`
	IP      string `json:"ip"`
}

type kafkaMessage struct {
	ID       string                 `json:"id"`
	Accepted int64                  `json:"accepted"`
	Sender   kafkaSender            `json:"sender"`
	Data     map[string]interface{} `json:"data"`
}

func (s *Server) rootHandler(w *HTTPResponse, r *http.Request, p *url.Values) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.rawResponse(w, http.StatusOK, []byte(`<!DOCTYPE html>
<html>
  <head>
    <meta charset="utf-8">
    <link href="http://yastatic.net/bootstrap/3.3.1/css/bootstrap.min.css" rel="stylesheet">
    <title>Endpoints | Kafka API v1</title>
  </head>
  <body>
    <div class="container"><h2>Kafka High-level API v1</h2><br>
        <table class="table">
          <tr>
            <th class="text-right">Write to Kafka</p></th>
            <td>POST</td>
            <td><code>{schema}://{host}/v1/topics/{topic}</code></td>
          </tr>
          <tr>
            <th class="text-right">Write to Kafka (Obsolete)</p></th>
            <td>POST</td>
            <td><code>{schema}://{host}/v1/topics/?queue={topic}</code></td>
          </tr>
        </table>
    </div>
  </body>
</html>`))
}

func (s *Server) pingHandler(w *HTTPResponse, r *http.Request, p *url.Values) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) notFoundHandler(w *HTTPResponse, r *http.Request, p *url.Values) {
	s.errorResponse(w, http.StatusNotFound, "404 page not found")
}

func (s *Server) notAllowedHandler(w *HTTPResponse, r *http.Request, p *url.Values) {
	s.errorResponse(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
}

func (s *Server) sendHandler(w *HTTPResponse, r *http.Request, p *url.Values) {
	msg, err := ioutil.ReadAll(r.Body)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Unable to read body: %s", err)
		return
	}

	queueTopic := p.Get("queue")
	if len(queueTopic) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Topic missing")
		return
	}

	var queuePartitions int64
	var queueBalance string
	var queueKeyComponents []string

	if val, ok := s.Cfg.Queue[queueTopic]; ok {
		queueBalance = val.Balance
		queueKeyComponents = val.Key
		queuePartitions = val.Partitions
	} else {
		s.errorResponse(w, http.StatusBadRequest, "Unknown topic")
		return
	}

	var m map[string]interface{}

	if err = json.Unmarshal(msg, &m); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Message must be JSON")
		return
	}

	partition, err := getPartition(queueBalance, queuePartitions, queueKeyComponents, m)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "%v", err)
		return
	}

	remoteip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Unable to get client IP address: %v", err)
		return
	}

	kmsg, err := json.Marshal(kafkaMessage{
		ID:       uuid.NewV4().String(),
		Accepted: time.Now().UnixNano(),
		Sender: kafkaSender{
			IP:      remoteip,
			Project: "",
			Cluster: "",
		},
		Data: m,
	})
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "%v", err)
		return
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	var endpointURL = fmt.Sprintf("%s/v1/topics/%s/%d", s.Cfg.Kafka.API, queueTopic, partition)
	for {
		resp, err := client.Post(
			endpointURL,
			"application/json",
			bytes.NewReader(kmsg),
		)
		if err == nil {
			resp.Body.Close()
			break
		}
		s.Log.Error("Unable to sent event to endpoint", "err", err)
	}

	s.successResponse(w, "OK")
}

func getPartition(balance string, maxPartitions int64, components []string, message map[string]interface{}) (uint64, error) {
	switch balance {
	case "key":
		partition, err := getPartitionByKey(components, message)
		if err != nil {
			return 0, err
		}
		return (partition % uint64(maxPartitions)), nil
	case "random":
		return uint64(rand.Int63n(maxPartitions)), nil
	}

	return 0, fmt.Errorf("Unsupported type of balance")
}

func getPartitionByKey(components []string, message map[string]interface{}) (uint64, error) {
	var num uint64
	var keyArr []string

	for _, k := range components {
		if val, ok := message[k]; ok {
			switch value := val.(type) {
			case string:
				keyArr = append(keyArr, value)
			default:
				return 0, fmt.Errorf("unsupported type of component: " + k)
			}
		} else {
			return 0, fmt.Errorf("can't find key components")
		}
	}

	checksum := sha1.Sum([]byte(strings.Join(keyArr, "/")))

	err := binary.Read(bytes.NewReader(checksum[:]), binary.LittleEndian, &num)
	if err != nil {
		return 0, err
	}

	return num, nil
}
