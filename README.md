# prometheus-statuspage-pusher

A daemon that queries Prometheus on an interval and pushes values to [StatusPage](https://statuspage.io).

## Usage

Every flag can also be provided as an environment variable (e.g. --prom_addr is $PROM_SP_PUSHER_PROM_ADDR).

```
Usage:
  prometheus-statuspage-pusher [flags]

Flags:
      --config string            local path the query config file
      --debug                    debug log verbosity
  -h, --help                     help for prometheus-statuspage-pusher
      --prom-addr string         address of the upstream gRPC service (default "http://127.0.0.1:9090")
      --push-interval duration   frequency that metrics are pushed to StatusPage (default: 30s) (default 30s)
      --sp-domain string         root domain used for StatusPage API (default: https://api.statuspage.io) (default "https://api.statuspage.io")
      --sp-page-id string        StatusPage Page ID
      --sp-token string          StatusPage OAuth Token
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
