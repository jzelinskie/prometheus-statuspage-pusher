# prometheus-statuspage-pusher

[![Docker Repository on Quay](https://quay.io/repository/jzelinskie/prometheus-statuspage-pusher/status "Docker Repository on Quay")](https://quay.io/repository/jzelinskie/prometheus-statuspage-pusher)

A daemon that queries Prometheus on an interval and pushes values to [StatusPage](https://statuspage.io).

- [YAML] configuration
- Logs with [zap]
- Exposes [Prometheus] and [pprof] metrics

[YAML]: https://yaml.org
[zap]: https://github.com/uber-go/zap
[Prometheus]: https://prometheus.io
[pprof]: https://github.com/google/pprof

## Usage

Every flag can also be provided as an environment variable (e.g. `--prom_url` is `$PROM_SP_PUSHER_PROM_URL`).

```
Usage:
  prometheus-statuspage-pusher [flags]

Flags:
      --config string                  local path the query config file (default: ./queries.yaml) (default "queries.yaml")
      --debug                          debug log verbosity (default: false)
  -h, --help                           help for prometheus-statuspage-pusher
      --internal-metrics-addr string   address that will serve prometheus and pprof data (default: :9090 (default ":9090")
      --prom-url string                address of the upstream gRPC service (default: http://127.0.0.1:9090) (default "http://127.0.0.1:9090")
      --push-interval duration         frequency that metrics are pushed to StatusPage (default: 30s) (default 30s)
      --sp-domain string               root domain used for StatusPage API (default: https://api.statuspage.io) (default "https://api.statuspage.io")
      --sp-page-id string              StatusPage Page ID
      --sp-token string                StatusPage OAuth Token
```

### Configuration

This program parses a YAML configuration file on startup that should be formatted as such:

```yaml
metricID: prometheus-query
```

Every query must return only a single element element vector.

Here's an example:

```
0fidj20fjm2n: avg(up{job="web"})
93njfmod02mf: |
  avg(
    rate(
      http_request_duration_microseconds{
        quantile="0.5",
        job="kubernetes-nodes"
      }[5m]
    )
  )
```
