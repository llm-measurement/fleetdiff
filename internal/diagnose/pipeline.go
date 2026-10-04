// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

var supportedComponentID = regexp.MustCompile(`^[a-z][a-z0-9_]*(/[A-Za-z0-9_.-]+)?$`)

func (c *checker) configuration(n *yaml.Node) {
	m := c.object(n, "receivers", "processors", "connectors", "exporters", "extensions", "service")
	groups := make(map[string]map[string]*yaml.Node)
	connectors := 0
	for _, group := range []string{"receivers", "processors", "connectors", "exporters", "extensions"} {
		groups[group] = c.object(m[group])
		items := ordered(m[group])
		for i := 0; i < len(items); i += 2 {
			name, config := items[i], items[i+1]
			if !supportedComponentID.MatchString(name.Value) {
				c.add("unsupported_mapping", "unsupported", name)
			}
			kind := componentType(name.Value)
			switch {
			case group == "connectors" && kind == "genaisketch":
				connectors++
				c.connector(config)
			case group == "receivers" && kind == "otlp":
				c.receiver(config)
			case group == "processors" && kind == "batch":
				p := c.object(config, "timeout", "send_batch_size", "send_batch_max_size")
				c.duration(p["timeout"], 0, time.Hour)
				c.integer(p["send_batch_size"], 0, 1<<30)
				c.integer(p["send_batch_max_size"], 0, 1<<30)
				if b := p["send_batch_max_size"]; b != nil {
					av := int64(8192)
					if a := p["send_batch_size"]; a != nil {
						av, _ = strconv.ParseInt(a.Value, 10, 64)
					}
					bv, _ := strconv.ParseInt(b.Value, 10, 64)
					if bv != 0 && bv < av {
						c.add("unsupported_mapping", "unsupported", config)
					}
				}
			case group == "processors" && kind == "memory_limiter":
				p := c.object(config, "check_interval", "limit_mib", "spike_limit_mib")
				c.duration(p["check_interval"], time.Millisecond, time.Hour)
				c.integer(p["limit_mib"], 1, 1<<30)
				c.integer(p["spike_limit_mib"], 0, 1<<30)
				if p["limit_mib"] == nil {
					c.add("unsupported_mapping", "unsupported", config)
				}
				if a, b := p["limit_mib"], p["spike_limit_mib"]; a != nil && b != nil {
					av, _ := strconv.ParseInt(a.Value, 10, 64)
					bv, _ := strconv.ParseInt(b.Value, 10, 64)
					if bv >= av {
						c.add("unsupported_mapping", "unsupported", config)
					}
				}
			case group == "exporters" && kind == "prometheus":
				p := c.object(config, "endpoint", "translation_strategy")
				c.endpoint(p["endpoint"])
				c.enum(p["translation_strategy"], "UnderscoreEscapingWithoutSuffixes", "UnderscoreEscapingWithSuffixes", "NoTranslation", "NoUTF8EscapingWithSuffixes")
			case group == "exporters" && (kind == "otlp" || kind == "otlphttp"):
				p := c.object(config, "endpoint", "headers", "tls", "timeout", "compression")
				if kind == "otlp" {
					c.endpoint(p["endpoint"])
				} else {
					u, err := url.Parse(c.string(p["endpoint"]))
					if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
						c.add("unsupported_mapping", "unsupported", config)
					}
				}
				headers := c.object(p["headers"])
				for _, name := range slices.Sorted(maps.Keys(headers)) {
					c.string(headers[name])
				}
				tls := c.object(p["tls"], "insecure", "insecure_skip_verify")
				for _, name := range []string{"insecure", "insecure_skip_verify"} {
					c.boolean(tls[name])
				}
				c.duration(p["timeout"], 0, time.Hour)
				c.enum(p["compression"], "gzip", "none")
			default:
				c.add("unsupported_component", "unsupported", name)
			}
		}
	}
	if connectors == 0 {
		c.add("missing_connector", "unsupported", n)
	}
	if connectors > 1 {
		c.add("multiple_producers", "unsupported", m["connectors"])
	}
	service := c.object(m["service"], "pipelines", "telemetry", "extensions")
	if m["service"] == nil {
		c.add("pipeline_wiring", "blocking", n)
	}
	telemetry := c.object(service["telemetry"], "logs", "metrics")
	logs := c.object(telemetry["logs"], "level", "encoding")
	c.enum(logs["level"], "debug", "info", "warn", "error", "dpanic", "panic", "fatal")
	c.enum(logs["encoding"], "console", "json")
	metrics := c.object(telemetry["metrics"], "level")
	c.enum(metrics["level"], "none", "basic", "normal", "detailed")
	for _, ref := range c.list(service["extensions"], false) {
		if groups["extensions"][c.string(ref)] == nil {
			c.add("pipeline_wiring", "blocking", ref)
		}
	}
	c.pipelines(service["pipelines"], groups)
}

func (c *checker) endpoint(n *yaml.Node) {
	_, port, err := net.SplitHostPort(c.string(n))
	p, pe := strconv.Atoi(port)
	if err != nil || pe != nil || p < 1 || p > 65535 {
		c.add("unsupported_mapping", "unsupported", n)
	}
}

func (c *checker) receiver(n *yaml.Node) {
	m := c.object(n, "protocols")
	protocols := c.object(m["protocols"], "grpc", "http")
	if len(protocols) == 0 {
		c.add("unsupported_mapping", "unsupported", n)
	}
	for _, name := range []string{"grpc", "http"} {
		if protocols[name] == nil {
			continue
		}
		p := c.object(protocols[name], "endpoint")
		if p["endpoint"] != nil {
			c.endpoint(p["endpoint"])
		}
	}
}

func (c *checker) pipelines(n *yaml.Node, groups map[string]map[string]*yaml.Node) {
	items := c.object(n)
	if len(items) == 0 {
		c.add("pipeline_wiring", "blocking", n)
	}
	traceUses, metricUses := make(map[string]int), make(map[string]int)
	used := make(map[string]bool)
	entries := ordered(n)
	for i := 0; i < len(entries); i += 2 {
		name, config := entries[i], entries[i+1]
		if !supportedComponentID.MatchString(name.Value) {
			c.add("unsupported_mapping", "unsupported", name)
		}
		signal := componentType(name.Value)
		if signal != "traces" && signal != "metrics" {
			c.add("unsupported_component", "unsupported", name)
		}
		p := c.object(config, "receivers", "processors", "exporters")
		for _, role := range []string{"receivers", "processors", "exporters"} {
			refs := c.list(p[role], false)
			if role != "processors" && len(refs) == 0 {
				c.add("pipeline_wiring", "blocking", config)
			}
			seen := make(map[string]bool)
			for _, ref := range refs {
				id := c.string(ref)
				if seen[id] {
					c.add("pipeline_wiring", "blocking", ref)
				}
				seen[id] = true
				if connector := groups["connectors"][id]; connector != nil && role != "processors" {
					used["connectors/"+id] = true
					if componentType(id) != "genaisketch" {
						c.add("unsupported_component", "unsupported", ref)
						continue
					}
					switch {
					case signal == "traces" && role == "exporters":
						traceUses[id]++
					case signal == "metrics" && role == "receivers":
						metricUses[id]++
					default:
						c.add("pipeline_wiring", "blocking", ref)
					}
					continue
				}
				if groups[role][id] == nil {
					c.add("pipeline_wiring", "blocking", ref)
					continue
				}
				used[role+"/"+id] = true
				if role == "exporters" && signal == "traces" && componentType(id) == "prometheus" {
					c.add("pipeline_wiring", "blocking", ref)
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(groups["connectors"])) {
		config := groups["connectors"][name]
		if componentType(name) != "genaisketch" {
			continue
		}
		if traceUses[name] == 0 || metricUses[name] == 0 {
			c.add("pipeline_wiring", "blocking", config)
		}
		if traceUses[name] > 1 || metricUses[name] > 1 {
			c.add("multiple_producers", "unsupported", config)
		}
	}
	for _, group := range []string{"receivers", "processors", "exporters"} {
		for _, name := range slices.Sorted(maps.Keys(groups[group])) {
			config := groups[group][name]
			if !used[group+"/"+name] {
				c.add("pipeline_wiring", "blocking", config)
			}
		}
	}
}
