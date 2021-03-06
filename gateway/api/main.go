package main

import (
	"flag"
	"os"
	"time"

	"github.com/kelseyhightower/envconfig"
	log "github.com/sirupsen/logrus"

	"eywa/gateway/api/server"
	"eywa/gateway/clients/k8s"
	"eywa/gateway/clients/registry"
	"eywa/gateway/hooks"
	"eywa/gateway/metrics"
	"eywa/gateway/types"
	"eywa/go-libs/broker"
	"eywa/go-libs/trigger"
)

// Config represents gateway startup configuration
type Config struct {
	NatsURL             string        `envconfig:"nats_url" default:"nats://nats.nats:4222"`
	StanClusterID       string        `envconfig:"stan_cluster_id" default:"stan"`
	StanClientID        string        `envconfig:"stan_client_id" default:"gateway"`
	PrometheusURL       string        `envconfig:"prometheus_url" default:"http://prometheus-operator-kube-p-prometheus.faas-system:9090"`
	RegistryURL         string        `envconfig:"registry_url" default:"http://registry.faas-system:9080"`
	CacheExpiryDuration time.Duration `envconfig:"cache_expiry_duration" default:"5s"`
	LimitCPUMin         string        `envconfig:"limit_cpu_min" default:"20m"`
	LimitCPUMax         string        `envconfig:"limit_cpu_max" default:"500m"`
	LimitMemMin         string        `envconfig:"limit_mem_min" default:"20Mi"`
	LimitMemMax         string        `envconfig:"limit_mem_max" default:"500Mi"`
	MongoDBHost         string        `envconfig:"mongodb_host" default:"mongodb.mongodb:27017"`
}

func main() {
	var conf Config
	err := envconfig.Process("", &conf)
	if err != nil {
		log.Fatalf("Failed to parse env: %s", err)
	}

	inCluster := flag.Bool("in-cluster", true, "(optional) running inside the cluser")
	debug := flag.Bool("debug", false, "(optional) set log level to debug")
	flag.Parse()

	log.SetLevel(log.ErrorLevel)
	if *debug {
		log.SetLevel(log.DebugLevel)
	}

	k8s, err := k8s.Setup(&k8s.Config{
		InCluster:           *inCluster,
		MongoDBHost:         conf.MongoDBHost,
		CacheExpiryDuration: conf.CacheExpiryDuration,
		LimitCPUMin:         conf.LimitCPUMin,
		LimitCPUMax:         conf.LimitCPUMax,
		LimitMemMin:         conf.LimitMemMin,
		LimitMemMax:         conf.LimitMemMax,
	})
	if err != nil {
		log.Fatalf("Failed to setup k8s client: %s", err)
	}

	metrics := metrics.Setup(k8s, &conf.PrometheusURL, time.Second*5)
	go metrics.FunctionWatcher()

	registry := registry.New(conf.RegistryURL)

	hostname, _ := os.Hostname()
	clientID := conf.StanClientID + broker.GetClientID(hostname)
	bc, err := broker.Connect(&broker.Config{
		NatsURL:        conf.NatsURL,
		ClusterID:      conf.StanClusterID,
		ClientID:       clientID,
		MaxReconnect:   100,
		ReconnectDelay: 5,
	})
	if err != nil {
		log.Fatalf("Failed to setup nats-streaming broker: %s", err)
	}

	eventHook := broker.NewLogHandler(types.LogsSubject, bc, hooks.EventHook, true)
	timelineHook := broker.NewLogHandler(types.LogsSubject, bc, hooks.TimelineHook, true)
	trigger.AddHook(eventHook, []trigger.Type{types.EventHookType})
	trigger.AddHook(timelineHook, []trigger.Type{types.TimelineHookType})

	params := &server.ContextParams{
		K8s:      k8s,
		Metrics:  metrics,
		Registry: registry,
		Broker:   bc,
	}

	server.Run(params)

	log.Exit(0)
}
