// Copyright 2014 Prometheus Team
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"runtime"

	"github.com/matttproud/golang_protobuf_extensions/ext"
	"github.com/prometheus/client_golang/text"

	dto "github.com/prometheus/client_model/go"
)

const (
	acceptHeader  = `application/vnd.google.protobuf;proto=io.prometheus.client.MetricFamily;encoding=delimited;q=0.7,text/plain;version=0.0.4;q=0.3`
	graphTemplate = `<html>
	<head>
		<script src="//cdnjs.cloudflare.com/ajax/libs/d3/3.4.11/d3.min.js"></script>
		<script src="//cdnjs.cloudflare.com/ajax/libs/rickshaw/1.4.6/rickshaw.min.js"></script>
	</head>
	<body>
		<div id="chart"></div>
		<script>
			var interval = 3000;
			var maxPoints = 100;
			var graph = new Rickshaw.Graph({
				element: document.querySelector("#chart"), 
				series: new Rickshaw.Series.FixedDuration([{ name: 'one' }], undefined, {
					timeInterval: interval,
					maxDataPoints: maxPoints,
					timeBase: new Date().getTime() / 1000
				})
			})

			var httpRequest;
			if (window.XMLHttpRequest) { // Mozilla, Safari, ...
				httpRequest = new XMLHttpRequest();
			} else if (window.ActiveXObject) { // IE 8 and older
		  		httpRequest = new ActiveXObject("Microsoft.XMLHTTP");
			}
		
			var ticker = window.setInterval(function() {
				httpRequest.onreadystatechange = updateGraph
				httpRequest.open('GET', window.location, true);
				httpRequest.setRequestHeader("Accept", "application/json")
				httpRequest.send(null);
			}, interval)
		    
			function updateGraph() {
				if (httpRequest.readyState === 4) {
					graph.series.addData(transform(httpRequest.responseText))
					graph.render();
		      		}
		    	}

			function transform(data) {
				json = JSON.parse(data);
				var palette = new Rickshaw.Color.Palette();
				
				var series = {}
				for (var mi in json) {
					if (json[mi]['type'] == "SUMMARY") {
						continue
					}
					for (var di in json[mi]['metrics']) {
						for (var key in json[mi]['metrics'][di]['labels']) {
							var name = json[mi]['name'] + '{' + key + '=' + json[mi]['metrics'][di]['labels'][key] + '}'
							series[name] = parseFloat(json[mi]['metrics'][di]['value'])
						}
					}
				}
				console.log(series)
				return series
			}
		</script>
	</body>
</html>`
)

var (
	addr = flag.String("addr", ":8000", "Address to listen on")

	templates = template.Must(template.New("graph").Parse(graphTemplate))
)

type metricFamily struct {
	Name    string        `json:"name"`
	Help    string        `json:"help"`
	Type    string        `json:"type"`
	Metrics []interface{} `json:"metrics,omitempty"` // Either metric or summary.
}

// metric is for all "single value" metrics.
type metric struct {
	Labels map[string]string `json:"labels,omitempty"`
	Value  string            `json:"value"`
}

type summary struct {
	Labels    map[string]string `json:"labels,omitempty"`
	Quantiles map[string]string `json:"quantiles,omitempty"`
	Count     string            `json:"count"`
	Sum       string            `json:"sum"`
}

func newMetricFamily(dtoMF *dto.MetricFamily) *metricFamily {
	mf := &metricFamily{
		Name:    dtoMF.GetName(),
		Help:    dtoMF.GetHelp(),
		Type:    dtoMF.GetType().String(),
		Metrics: make([]interface{}, len(dtoMF.Metric)),
	}
	isSummary := dtoMF.GetType() == dto.MetricType_SUMMARY
	for i, m := range dtoMF.Metric {
		if isSummary {
			mf.Metrics[i] = summary{
				Labels:    makeLabels(m),
				Quantiles: makeQuantiles(m),
				Count:     fmt.Sprint(m.GetSummary().GetSampleCount()),
				Sum:       fmt.Sprint(m.GetSummary().GetSampleSum()),
			}
		} else {
			mf.Metrics[i] = metric{
				Labels: makeLabels(m),
				Value:  fmt.Sprint(getValue(m)),
			}
		}
	}
	return mf
}

func getValue(m *dto.Metric) float64 {
	if m.Gauge != nil {
		return m.GetGauge().GetValue()
	}
	if m.Counter != nil {
		return m.GetCounter().GetValue()
	}
	if m.Untyped != nil {
		return m.GetUntyped().GetValue()
	}
	return 0.
}

func makeLabels(m *dto.Metric) map[string]string {
	result := map[string]string{}
	for _, lp := range m.Label {
		result[lp.GetName()] = lp.GetValue()
	}
	return result
}

func makeQuantiles(m *dto.Metric) map[string]string {
	result := map[string]string{}
	for _, q := range m.GetSummary().Quantile {
		result[fmt.Sprint(q.GetQuantile())] = fmt.Sprint(q.GetValue())
	}
	return result
}

func fetchMetricFamilies(url string, ch chan<- *dto.MetricFamily) {
	defer close(ch)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatalf("creating GET request for URL %q failed: %s", url, err)
	}
	req.Header.Add("Accept", acceptHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("executing GET request for URL %q failed: %s", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("GET request for URL %q returned HTTP status %s", url, resp.Status)
	}

	mediatype, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err == nil && mediatype == "application/vnd.google.protobuf" &&
		params["encoding"] == "delimited" &&
		params["proto"] == "io.prometheus.client.MetricFamily" {
		for {
			mf := &dto.MetricFamily{}
			if _, err = ext.ReadDelimited(resp.Body, mf); err != nil {
				if err == io.EOF {
					break
				}
				log.Fatalln("reading metric family protocol buffer failed:", err)
			}
			ch <- mf
		}
	} else {
		// We could do further content-type checks here, but the
		// fallback for now will anyway be the text format
		// version 0.0.4, so just go for it and see if it works.
		var parser text.Parser
		metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
		if err != nil {
			log.Fatalln("reading text format failed:", err)
		}
		for _, mf := range metricFamilies {
			ch <- mf
		}
	}
}

func handleJson(w http.ResponseWriter, r *http.Request) {
	mfChan := make(chan *dto.MetricFamily, 1024)
	if len(r.URL.Path) < 2 {
		http.Error(w, "expect exporter url in path", http.StatusBadGateway)
		return
	}
	exporterUrl := fmt.Sprintf("http://%s", r.URL.Path[1:])

	go fetchMetricFamilies(exporterUrl, mfChan)

	result := []*metricFamily{}
	for mf := range mfChan {
		result = append(result, newMetricFamily(mf))
	}
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(result); err != nil {
		log.Println(err.Error())
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	log.Println("<", r.URL)
	accept := r.Header.Get("Accept")
	if accept == "application/json" {
		handleJson(w, r)
		return
	}
	templates.Execute(w, nil)
}

func main() {
	flag.Parse()
	http.HandleFunc("/", handleRequest)
	runtime.GOMAXPROCS(2) // Why?

	log.Fatal(http.ListenAndServe(*addr, nil))
}
