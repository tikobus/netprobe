package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type BasicAuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type ServerConfig struct {
	Addr      string          `yaml:"addr"`
	BasicAuth BasicAuthConfig `yaml:"basic_auth"`
}

type StorageConfig struct {
	DBPath string `yaml:"db_path"`
	// DBRetainDays 原始探测记录保留天数（0 = 永不清理）
	DBRetainDays int `yaml:"db_retain_days"`
}

type LogConfig struct {
	Dir        string `yaml:"dir"`
	RetainDays int    `yaml:"retain_days"`
	Level      string `yaml:"level"`
}

type ProberConfig struct {
	Concurrency     int `yaml:"concurrency"`
	DefaultInterval int `yaml:"default_interval"`
	DefaultTimeout  int `yaml:"default_timeout"`
	DefaultRetries  int `yaml:"default_retries"`
}

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Storage StorageConfig `yaml:"storage"`
	Log     LogConfig     `yaml:"log"`
	Prober  ProberConfig  `yaml:"prober"`
}

// Default 返回内置默认配置。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:      ":8080",
			BasicAuth: BasicAuthConfig{Enabled: false, Username: "admin", Password: "change-me"},
		},
		Storage: StorageConfig{DBPath: "data/netprobe.db", DBRetainDays: 7},
		Log:     LogConfig{Dir: "logs", RetainDays: 30, Level: "info"},
		Prober:  ProberConfig{Concurrency: 32, DefaultInterval: 30, DefaultTimeout: 5, DefaultRetries: 2},
	}
}

// Load 读取 YAML 配置文件（可为空字符串，表示全默认），叠加环境变量后校验。
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// applyEnv 环境变量覆盖常用项，便于容器/临时部署。
func applyEnv(cfg *Config) {
	if v := os.Getenv("NETPROBE_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("NETPROBE_DB_PATH"); v != "" {
		cfg.Storage.DBPath = v
	}
	if v := os.Getenv("NETPROBE_LOG_DIR"); v != "" {
		cfg.Log.Dir = v
	}
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Server.Addr) == "" {
		return fmt.Errorf("server.addr 不能为空")
	}
	if c.Server.BasicAuth.Enabled {
		if c.Server.BasicAuth.Username == "" || c.Server.BasicAuth.Password == "" {
			return fmt.Errorf("basic_auth 开启时 username/password 不能为空")
		}
	}
	if strings.TrimSpace(c.Storage.DBPath) == "" {
		return fmt.Errorf("storage.db_path 不能为空")
	}
	if c.Storage.DBRetainDays < 0 {
		return fmt.Errorf("storage.db_retain_days 必须 >= 0")
	}
	if c.Log.RetainDays < 1 {
		return fmt.Errorf("log.retain_days 必须 >= 1")
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
		c.Log.Level = strings.ToLower(c.Log.Level)
	default:
		return fmt.Errorf("log.level 非法: %q", c.Log.Level)
	}
	if c.Prober.Concurrency < 1 {
		return fmt.Errorf("prober.concurrency 必须 >= 1")
	}
	if c.Prober.DefaultInterval < 1 || c.Prober.DefaultTimeout < 1 || c.Prober.DefaultRetries < 0 {
		return fmt.Errorf("prober 默认参数非法（interval/timeout >=1, retries >=0）")
	}
	return nil
}
