/*
* Copyright (C) 2015 Alexey Gladkov <gladkov.alexey@gmail.com>
*
* This file is covered by the GNU General Public License,
* which should be included with kafka-http-api as the file COPYING.
*/

package main

import (
	log "github.com/Sirupsen/logrus"

	"strings"
)

type CfgLogLevel struct {
	log.Level
}

func (d *CfgLogLevel) UnmarshalText(data []byte) (err error) {
	d.Level, err = log.ParseLevel(strings.ToLower(string(data)))
	return
}

type ConfigGlobal struct {
	Address string
	Logfile    string
	Pidfile    string
	GoMaxProcs int
	MaxConns   int64
}

type ConfigLogging struct {
	Level            CfgLogLevel
	DisableColors    bool
	DisableTimestamp bool
	FullTimestamp    bool
	DisableSorting   bool
}

type ConfigKafka struct {
	API string
}

type ConfigQueue struct {
	Partitions int64
	Balance    string
	Key        []string
}

type Config struct {
	Global  ConfigGlobal
	Logging ConfigLogging
	Kafka   ConfigKafka
	Queue   map[string]ConfigQueue
}

// SetDefaults applies default values to config structure.
func (c *Config) SetDefaults() {
	c.Global.GoMaxProcs = 0
	c.Global.Logfile = "/var/log/kafka-high-api.log"
	c.Global.Pidfile = "/run/kafka-high-api.pid"

	c.Logging.Level.Level = log.InfoLevel
	c.Logging.DisableColors = true
	c.Logging.DisableTimestamp = false
	c.Logging.FullTimestamp = true
	c.Logging.DisableSorting = true
}
