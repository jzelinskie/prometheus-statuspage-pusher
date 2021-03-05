package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	"github.com/jzelinskie/cobrautil"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	prommodel "github.com/prometheus/common/model"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func init() {
	prometheus.MustRegister(pushCounter)
}

var pushCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "statuspage_pusher_pushes",
		Help: "Responses for the various calls",
	},
	[]string{"metric_id", "response_code"},
)

func main() {
	var rootCmd = &cobra.Command{
		Use:               "prometheus-statuspage-pusher",
		PersistentPreRunE: cobrautil.SyncViperPreRunE("PROM_SP_PUSHER"),
		Run:               rootRun,
	}

	rootCmd.Flags().String("prom-url", "http://127.0.0.1:9090", "address of the upstream gRPC service (default: http://127.0.0.1:9090)")
	rootCmd.Flags().String("sp-domain", "https://api.statuspage.io", "root domain used for StatusPage API (default: https://api.statuspage.io)")
	rootCmd.Flags().String("sp-page-id", "", "StatusPage Page ID")
	rootCmd.Flags().String("sp-token", "", "StatusPage OAuth Token")
	rootCmd.Flags().String("config", "queries.yaml", "local path the query config file (default: ./queries.yaml)")
	rootCmd.Flags().Duration("push-interval", 30*time.Second, "frequency that metrics are pushed to StatusPage (default: 30s)")
	rootCmd.Flags().String("internal-metrics-addr", ":9090", "address that will serve prometheus and pprof data (default: :9090")
	rootCmd.Flags().Bool("debug", false, "debug log verbosity (default: false)")

	rootCmd.Execute()
}

func rootRun(cmd *cobra.Command, args []string) {
	logger, _ := zap.NewProduction()
	if cobrautil.MustGetBool(cmd, "debug") {
		logger, _ = zap.NewDevelopment()
	}
	defer logger.Sync()

	configmap := parseConfig(logger, cobrautil.MustGetStringExpanded(cmd, "config"))

	client, err := promapi.NewClient(promapi.Config{Address: cobrautil.MustGetString(cmd, "prom-url")})
	if err != nil {
		logger.Fatal("failed to init prom client", zap.Error(err))
	}
	api := promv1.NewAPI(client)

	signalctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)

	httpsrv := &http.Server{Addr: cobrautil.MustGetString(cmd, "internal-metrics-addr")}
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		httpsrv.Handler = mux

		if err := httpsrv.ListenAndServe(); err != http.ErrServerClosed {
			logger.Fatal("failed while serving prometheus", zap.Error(err))
		}
	}()

	domain := cobrautil.MustGetString(cmd, "sp-domain")
	pageID := cobrautil.MustGetString(cmd, "sp-page-id")
	token := cobrautil.MustGetString(cmd, "sp-token")

	for {
		select {
		case <-time.After(cobrautil.MustGetDuration(cmd, "push-interval")):
			now := time.Now()
			for metricID, query := range configmap {
				value := queryProm(logger, api, query, now)
				pushValueToStatusPage(logger, domain, token, pageID, metricID, value, now)
			}
		case <-signalctx.Done():
			if err := httpsrv.Close(); err != nil {
				logger.Fatal("failed while shutting down metrics server", zap.Error(err))
			}
			return
		}
	}
}

func parseConfig(logger *zap.Logger, path string) map[string]string {
	contents, err := os.ReadFile(path)
	if err != nil {
		logger.Fatal("failed to read config file", zap.Error(err))
	}

	configmap := make(map[string]string)
	if err := yaml.Unmarshal(contents, &configmap); err != nil {
		logger.Fatal("failed to parse config file", zap.Error(err))
	}

	return configmap
}

func queryProm(logger *zap.Logger, api promv1.API, query string, now time.Time) float64 {
	resp, _, err := api.Query(context.Background(), query, now)
	if err != nil {
		logger.Fatal("failed to query prometheus", zap.Error(err))
	}

	vec := resp.(prommodel.Vector)
	if l := vec.Len(); l != 1 {
		logger.Fatal(
			"expected query to return a single value",
			zap.String("query", query),
			zap.Int("vector size", l),
		)
	}

	return float64(vec[0].Value)
}

func pushValueToStatusPage(logger *zap.Logger, domain, token, pageID, metricID string, value float64, now time.Time) {
	requestURL := fmt.Sprintf(
		"%s/v1/pages/%s/metrics/%s/data.json",
		domain,
		pageID,
		metricID,
	)

	urlValues := url.Values{
		"data[timestamp]": []string{strconv.FormatInt(now.Unix(), 10)},
		"data[value]":     []string{strconv.FormatFloat(value, 'f', -1, 64)},
	}

	req, err := http.NewRequest("POST", requestURL, strings.NewReader(urlValues.Encode()))
	if err != nil {
		logger.Fatal("failed to create request", zap.Error(err))
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Debatable whether or not this should be fatal.
		logger.Warn("failed calling StatusPage API", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	pushCounter.WithLabelValues(metricID, resp.Status).Add(1)

	if resp.StatusCode/100 != 2 {
		logger.Warn("non-2xx response from StatusPage API", zap.Int("status code", resp.StatusCode))
	}
}
