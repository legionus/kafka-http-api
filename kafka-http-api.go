/*
* Copyright (C) 2015 Alexey Gladkov <gladkov.alexey@gmail.com>
*
* This file is covered by the GNU General Public License,
* which should be included with kafka-http-api as the file COPYING.
*/

package main

import (
	log "github.com/Sirupsen/logrus"
	"github.com/BurntSushi/toml"

	_ "net/http/pprof"

	"encoding/json"
	"sync/atomic"
	"flag"
	"fmt"
	"regexp"
	"runtime"
	"net/http"
	"net/url"
	"time"
)

var (
	addr   = flag.String("addr", "", "The address to bind to")
	config = flag.String("config", "", "Path to configuration file")
)

// HTTPResponse is a wrapper for http.ResponseWriter
type HTTPResponse struct {
	http.ResponseWriter

	HTTPStatus     int
	HTTPError      string
	ResponseLength int64
}

func (resp *HTTPResponse) Write(b []byte) (n int, err error) {
	n, err = resp.ResponseWriter.Write(b)
	if err == nil {
		resp.ResponseLength += int64(len(b))
	}
	return
}

// JSONResponse is a template for all the proxy answers.
type JSONResponse struct {
	// Response type. It can be either "success" or "error".
	Status string `json:"status"`

	// The response data. It depends on the type of response.
	Data interface{} `json:"data"`
}

// JSONErrorData is a template for error answers.
type JSONErrorData struct {
	// HTTP status code.
	Code int `json:"code"`

	// Human readable error message.
	Message string `json:"message"`
}

// JSONErrorOutOfRange contains a template for response if the requested offset out of range.
type JSONErrorOutOfRange struct {
	// HTTP status code.
	Code int `json:"code"`

	// Human readable error message.
	Message string `json:"message"`

	Topic        string `json:"topic"`
	Partition    int32  `json:"partition"`
	OffsetOldest int64  `json:"offsetfrom"`
	OffsetNewest int64  `json:"offsetto"`
}

// ConnTrack used to track the number of connections.
type ConnTrack struct {
	ConnID int64
	Conns  int64
}

type Server struct {
	lastConnID int64
	connsCount int64

	Cfg Config
	Log *ServerLogger
}

func (s *Server) newConnTrack(r *http.Request) ConnTrack {
	cl := ConnTrack{
		ConnID: atomic.AddInt64(&s.lastConnID, 1),
	}

	conns := atomic.AddInt64(&s.connsCount, 1)
	log.Debugf("Opened connection %d (total=%d) [%s %s]", cl.ConnID, conns, r.Method, r.URL)

	cl.Conns = conns
	return cl
}

func (s *Server) closeConnTrack(cl ConnTrack) {
	conns := atomic.AddInt64(&s.connsCount, -1)
	log.Debugf("Closed connection %d (total=%d)", cl.ConnID, conns)
}

func (s *Server) rawResponse(resp *HTTPResponse, status int, b []byte) {
	resp.HTTPStatus = status

	resp.WriteHeader(status)
	resp.Write(b)
}

func (s *Server) writeResponse(w *HTTPResponse, status int, v *JSONResponse) {
	w.Header().Set("Content-Type", "application/json")

	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.Errorln("Unable to marshal result:", err)
		return
	}

	s.rawResponse(w, status, b)
}

func (s *Server) successResponse(w *HTTPResponse, m interface{}) {
	resp := &JSONResponse{
		Status: "success",
		Data:   m,
	}
	s.writeResponse(w, http.StatusOK, resp)
}

func (s *Server) errorResponse(w *HTTPResponse, status int, format string, args ...interface{}) {
	w.HTTPError = fmt.Sprintf(format, args...)

	resp := &JSONResponse{
		Status: "error",
		Data: &JSONErrorData{
			Code:    status,
			Message: w.HTTPError,
		},
	}
	log.Debugf("%+v", resp.Data)
	s.writeResponse(w, status, resp)
}

func (s *Server) errorOutOfRange(w *HTTPResponse, topic string, partition int32, offsetFrom int64, offsetTo int64) {
	status := http.StatusRequestedRangeNotSatisfiable
	resp := &JSONResponse{
		Status: "error",
		Data: &JSONErrorOutOfRange{
			Code:         status,
			Message:      fmt.Sprintf("Offset out of range (%v, %v)", offsetFrom, offsetTo),
			Topic:        topic,
			Partition:    partition,
			OffsetOldest: offsetFrom,
			OffsetNewest: offsetTo,
		},
	}
	log.Debugf("%+v", resp.Data)
	s.writeResponse(w, status, resp)
}

func (s *Server) Run() error {
	type httpHandler struct {
		LimitConns  bool
		Regexp      *regexp.Regexp
		GETHandler  func(*HTTPResponse, *http.Request, *url.Values)
		POSTHandler func(*HTTPResponse, *http.Request, *url.Values)
	}

	handlers := []httpHandler{
		httpHandler{
			Regexp:      regexp.MustCompile("^/v1/topics/(?P<queue>[A-Za-z0-9_-]+)/?$"),
			LimitConns:  true,
			GETHandler:  s.notAllowedHandler,
			POSTHandler: s.sendHandler,
		},
		httpHandler{
			Regexp:      regexp.MustCompile("^/v1/topics/?$"),
			LimitConns:  true,
			GETHandler:  s.notAllowedHandler,
			POSTHandler: s.sendHandler,
		},
		httpHandler{
			Regexp:      regexp.MustCompile("^/ping$"),
			LimitConns:  false,
			GETHandler:  s.pingHandler,
			POSTHandler: s.notAllowedHandler,
		},
		httpHandler{
			Regexp:      regexp.MustCompile("^/$"),
			LimitConns:  false,
			GETHandler:  s.rootHandler,
			POSTHandler: s.notAllowedHandler,
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/debug/vars", http.DefaultServeMux)
	mux.Handle("/debug/pprof/", http.DefaultServeMux)
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		reqTime := time.Now()
		resp := &HTTPResponse{w, http.StatusOK, "", 0}

		defer func() {
			e := log.NewEntry(log.StandardLogger()).WithFields(log.Fields{
				"stop":    time.Now().String(),
				"start":   reqTime.String(),
				"method":  req.Method,
				"addr":    req.RemoteAddr,
				"reqlen":  req.ContentLength,
				"resplen": resp.ResponseLength,
				"status":  resp.HTTPStatus,
			})

			if resp.HTTPStatus >= 500 {
				e = e.WithField("error", resp.HTTPError)
			}

			e.Info(req.URL)
		}()

		cl := s.newConnTrack(req)
		defer s.closeConnTrack(cl)

		p := req.URL.Query()

		for _, a := range handlers {
			match := a.Regexp.FindStringSubmatch(req.URL.Path)
			if match == nil {
				continue
			}

			if a.LimitConns && s.Cfg.Global.MaxConns > 0 && cl.Conns >= s.Cfg.Global.MaxConns {
				s.errorResponse(resp, http.StatusServiceUnavailable, "Too many connections")
				return
			}

			for i, name := range a.Regexp.SubexpNames() {
				if i == 0 {
					continue
				}
				p.Set(name, match[i])
			}

			switch req.Method {
			case "GET":
				a.GETHandler(resp, req, &p)
			case "POST":
				a.POSTHandler(resp, req, &p)
			default:
				s.notAllowedHandler(resp, req, &p)
			}
			return
		}

		s.notFoundHandler(resp, req, &p)
		return
	})

	httpServer := &http.Server{
		Addr:    s.Cfg.Global.Address,
		Handler: mux,
	}

	s.Log.Info("Server ready")
	return httpServer.ListenAndServe()
}

func main() {
	flag.Parse()

	if *config == "" {
		*config = "/etc/kafka-http-api.cfg"
	}

	cfg := Config{}
	cfg.SetDefaults()

	if _, err := toml.DecodeFile(*config, &cfg); err != nil {
		log.Fatal(err)
	}

	log.SetLevel(cfg.Logging.Level.Level)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp:    cfg.Logging.FullTimestamp,
		DisableTimestamp: cfg.Logging.DisableTimestamp,
		DisableColors:    cfg.Logging.DisableColors,
		DisableSorting:   cfg.Logging.DisableSorting,
	})

	pidfile, err := OpenPidfile(cfg.Global.Pidfile)
	if err != nil {
		log.Fatal("Unable to open pidfile: ", err.Error())
	}
	defer pidfile.Close()

	if err := pidfile.Check(); err != nil {
		log.Fatal("Check failed: ", err.Error())
	}

	if err := pidfile.Write(); err != nil {
		log.Fatal("Unable to write pidfile: ", err.Error())
	}

	logfile, err := OpenLogfile(cfg.Global.Logfile)
	if err != nil {
		log.Fatal("Unable to open log: ", err.Error())
	}
	defer logfile.Close()
	log.SetOutput(logfile)

	if cfg.Global.GoMaxProcs == 0 {
		cfg.Global.GoMaxProcs = runtime.NumCPU()
	}
	runtime.GOMAXPROCS(cfg.Global.GoMaxProcs)

	s := &Server{
		Cfg: cfg,
		Log: &ServerLogger{
			subsys: "server",
		},

	}
	s.Log.Fatal("%v", s.Run())
}
