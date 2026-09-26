package configv2

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Configuration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Configuration, error) {
	var cfg Configuration
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return &cfg, nil
}

func (c *Configuration) ApplyDefaults() {
	for name, instance := range c.Desync.Instances {
		if instance.Table == "" {
			instance.Table = "inet nfqueue-" + name
			c.Desync.Instances[name] = instance
		}
	}
	if c.Runtime.StateDir == "" {
		c.Runtime.StateDir = DefaultStateDir
	}
	if c.Runtime.RuntimeDir == "" {
		c.Runtime.RuntimeDir = DefaultRuntimeDir
	}
	if c.Runtime.ProbeInterval == 0 {
		c.Runtime.ProbeInterval = Duration(10 * time.Second)
	}
	if c.Runtime.RefreshInterval == 0 {
		c.Runtime.RefreshInterval = Duration(5 * time.Minute)
	}
}

func (c *Configuration) Validate() error {
	if c.Proxy.Engine != "" && !contains(ProxyEngines, c.Proxy.Engine) {
		return fmt.Errorf("unsupported proxy engine %q", c.Proxy.Engine)
	}
	if c.Proxy.Mode != "" && !contains(ProxyModes, c.Proxy.Mode) {
		return fmt.Errorf("unsupported proxy mode %q", c.Proxy.Mode)
	}
	if c.Runtime.Mode != "" && !contains(ProxyModes, c.Runtime.Mode) {
		return fmt.Errorf("unsupported runtime mode %q", c.Runtime.Mode)
	}
	for name, instance := range c.Desync.Instances {
		if !contains(DesyncEngines, instance.Engine) {
			return fmt.Errorf("desync instance %q has unknown engine %q", name, instance.Engine)
		}
		if instance.Table != "" && !nftTablePattern.MatchString(instance.Table) {
			return fmt.Errorf("desync instance %q has invalid nft table %q", name, instance.Table)
		}
	}
	for name, source := range c.DataSources {
		if source.Type == "" {
			return fmt.Errorf("data source %q has no type", name)
		}
		if source.Refresh < 0 {
			return fmt.Errorf("data source %q has a negative refresh interval", name)
		}
	}
	for _, group := range []struct {
		kind  string
		lists map[string]List
	}{
		{"domains", c.Lists.Domains},
		{"ips", c.Lists.IPs},
	} {
		for name, list := range group.lists {
			if list.Source == "" && len(list.Files) == 0 && len(list.URLs) == 0 && len(list.Domains) == 0 && len(list.CIDRs) == 0 {
				return fmt.Errorf("%s list %q has no sources", group.kind, name)
			}
			if list.Source != "" {
				if _, ok := c.DataSources[list.Source]; !ok {
					return fmt.Errorf("%s list %q references unknown data source %q", group.kind, name, list.Source)
				}
				if list.Dataset == "" || list.Category == "" {
					return fmt.Errorf("%s list %q needs both dataset and category", group.kind, name)
				}
			}
		}
	}
	for name, pool := range c.Pools {
		if pool.Type == "" {
			return fmt.Errorf("pool %q has no type", name)
		}
	}
	return nil
}
