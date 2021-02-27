package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var rootCmd = &cobra.Command{
	Use: "prometheus-statuspage-pusher",
	Run: rootRun,
}

func main() {
	viper.SetDefault("PROM_ADDR", "http://127.0.0.1:9090")
	viper.SetDefault("SP_DOMAIN", "https://api.statuspage.io")
	viper.SetDefault("SP_PAGE_ID", "")
	viper.SetDefault("SP_TOKEN", "")
	viper.SetDefault("CONFIG", "queries.yaml")
	viper.SetDefault("PUSH_INTERVAL", 30*time.Second)
	viper.SetDefault("DEBUG", false)

	rootCmd.Flags().String("prom-addr", viper.GetString("PROM_ADDR"), "address of the upstream gRPC service")
	rootCmd.Flags().String("sp-domain", viper.GetString("SP_DOMAIN"), "root domain used for StatusPage API (default: https://api.statuspage.io)")

	rootCmd.Flags().String("sp-page-id", viper.GetString("SP_PAGE_ID"), "StatusPage Page ID")
	rootCmd.Flags().String("sp-token", viper.GetString("SP_TOKEN"), "StatusPage OAuth Token")
	rootCmd.Flags().String("config", viper.GetString("CONFIG_PATH"), "local path the query config file")
	rootCmd.Flags().Duration("push-interval", viper.GetDuration("PUSH_INTERVAL"), "frequency that metrics are pushed to StatusPage (default: 30s)")
	rootCmd.Flags().Bool("debug", viper.GetBool("DEBUG"), "debug log verbosity")

	viper.BindPFlag("PROM_ADDR", rootCmd.Flags().Lookup("prom-addr"))
	viper.BindPFlag("SP_DOMAIN", rootCmd.Flags().Lookup("sp-url"))
	viper.BindPFlag("SP_PAGE_ID", rootCmd.Flags().Lookup("sp-page-id"))
	viper.BindPFlag("SP_TOKEN", rootCmd.Flags().Lookup("sp-token"))
	viper.BindPFlag("CONFIG", rootCmd.Flags().Lookup("config"))
	viper.BindPFlag("PUSH_INTERVAL", rootCmd.Flags().Lookup("push-interval"))
	viper.BindPFlag("DEBUG", rootCmd.Flags().Lookup("debug"))

	viper.SetEnvPrefix("PROM_SP_PUSHER")
	viper.AutomaticEnv()

	rootCmd.Execute()
}

func rootRun(cmd *cobra.Command, args []string) {
	logger, _ := zap.NewProduction()
	if viper.GetBool("debug") {
		logger, _ = zap.NewDevelopment()
	}
	defer logger.Sync()

	// TODO(jzelinskie): uncomment when this issue is resolved:
	// https://github.com/spf13/viper/issues/695
	// logger.Debug("parsed settings", zap.String("settings", fmt.Sprintf("%#v", viper.AllSettings())))

	configmap := parseConfig(logger, viper.GetString("CONFIG"))

	client, err := promapi.NewClient(promapi.Config{Address: viper.GetString("PROM_ADDR")})
	if err != nil {
		logger.Fatal("failed to init prom client", zap.Error(err))
	}
	api := promv1.NewAPI(client)

	signalctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)

	for {
		select {
		case <-time.After(viper.GetDuration("PUSH_INTERVAL")):
			now := time.Now()
			for metricID, query := range configmap {
				value := queryProm(logger, api, query, now)
				pushValueToStatusPage(logger, metricID, value, now)
			}
		case <-signalctx.Done():
			return
		}
	}
}

func parseConfig(logger *zap.Logger, path string) map[string]string {
	contents, err := os.ReadFile(viper.GetString("CONFIG"))
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

func pushValueToStatusPage(logger *zap.Logger, metricID string, value float64, now time.Time) {
	requestURL := viper.GetString("SP_DOMAIN") + fmt.Sprintf(
		"/v1/pages/%s/metrics/%s/data.json",
		viper.GetString("SP_PAGE_ID"),
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
	req.Header.Set("Authorization", "OAuth "+viper.GetString("SP_TOKEN"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Warn("failed calling StatusPage API", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		logger.Warn("non-2xx response from StatusPage API", zap.Int("status code", resp.StatusCode))
	}
}
