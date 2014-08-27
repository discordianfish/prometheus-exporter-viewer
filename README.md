# prometheus-exporter-viewer
A web ui to scrape a Prometheus client/exporter and draw real time graphs.

This tool takes the provided path as exporter address. A requests to
`http://localhost:8000/foo.example.com:9080/metrics` will show a graph
of metrics exported by `http://foo.example.com:9080/metrics`.

