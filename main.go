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

	"github.com/cenkalti/backoff/v4"
	"github.com/ghodss/yaml"
	"github.com/jzelinskie/cobrautil"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	prommodel "github.com/prometheus/common/model"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var pushCounter = promauto.NewCounterVec(
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
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if cobrautil.MustGetBool(cmd, "debug") {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
		log.Info().Str("new level", "trace").Msg("set log level")
	}

	configmap, err := parseConfig(cobrautil.MustGetStringExpanded(cmd, "config"))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse config")
	}

	client, err := promapi.NewClient(promapi.Config{Address: cobrautil.MustGetString(cmd, "prom-url")})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init prom client")
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

		log.Info().Str("addr", httpsrv.Addr).Msg("metrics and pprof server listening")
		if err := httpsrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("failed while serving prometheus")
		}
	}()

	domain := cobrautil.MustGetString(cmd, "sp-domain")
	pageID := cobrautil.MustGetString(cmd, "sp-page-id")
	token := cobrautil.MustGetString(cmd, "sp-token")

	backoffInterval := backoff.NewExponentialBackOff()
	backoffInterval.InitialInterval = cobrautil.MustGetDuration(cmd, "push-interval")
	ticker := time.After(backoffInterval.InitialInterval)

	log.Info().Dur("interval", backoffInterval.InitialInterval).Msg("began pushing metrics")
	for {
		select {
		case <-ticker:
			now := time.Now()
			for metricID, query := range configmap {
				value, err := queryProm(api, query, now)
				if err != nil {
					log.Fatal().Err(err).Msg("failed to query prometheus")
				}

				nextPush := backoffInterval.InitialInterval
				if pushValueToStatusPage(domain, token, pageID, metricID, value, now) {
					nextPush = backoffInterval.NextBackOff()
				} else {
					backoffInterval.Reset()
				}
				ticker = time.After(nextPush)
			}
		case <-signalctx.Done():
			if err := httpsrv.Close(); err != nil {
				log.Fatal().Err(err).Msg("failed while shutting down metrics server")
			}
			return
		}
	}
}

func parseConfig(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	configmap := make(map[string]string)
	if err := yaml.Unmarshal(contents, &configmap); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return configmap, nil
}

func queryProm(api promv1.API, query string, now time.Time) (float64, error) {
	resp, _, err := api.Query(context.Background(), query, now)
	if err != nil {
		return 0, fmt.Errorf("failed to query prometheus: %w", err)
	}

	vec := resp.(prommodel.Vector)
	if l := vec.Len(); l != 1 {
		return 0, fmt.Errorf("expected query to return a single value")
	}

	return float64(vec[0].Value), nil
}

func pushValueToStatusPage(domain, token, pageID, metricID string, value float64, now time.Time) (backoff bool) {
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
		log.Fatal().Err(err).Msg("failed to create request")
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Debatable whether or not this should be fatal.
		log.Warn().Err(err).Msg("failed calling StatusPage API")
		return true
	}
	defer resp.Body.Close()

	pushCounter.WithLabelValues(metricID, resp.Status).Add(1)

	if resp.StatusCode/100 != 2 {
		log.Warn().Int("status code", resp.StatusCode).Str("metric", metricID).Msg("non-2xx response from StatusPage API")
		return true
	}
	log.Info().Int("status code", resp.StatusCode).Str("metric", metricID).Msg("2xx response from StatusPage API")
	return false
}
