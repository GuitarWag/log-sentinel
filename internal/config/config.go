package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

type AppConfig struct {
	Name         string   `yaml:"name"`
	Source       string   `yaml:"source"`
	PollInterval Duration `yaml:"poll_interval"`

	Project string `yaml:"project"`
	Filter  string `yaml:"filter"`

	Region        string `yaml:"region"`
	LogGroup      string `yaml:"log_group"`
	FilterPattern string `yaml:"filter_pattern"`

	LogFile string `yaml:"log_file"`
}

type AWSConfig struct {
	Region       string `yaml:"region"`
	BedrockModel string `yaml:"bedrock_model"`
	SQSQueueURL  string `yaml:"sqs_queue_url"`
	EndpointURL  string `yaml:"endpoint_url"`
}

type MockConfig struct {
	UseRuleClassifier bool `yaml:"use_rule_classifier"`
}

type WorkerConfig struct {
	App         string         `yaml:"app"`
	Concurrency int            `yaml:"concurrency"`
	Actions     []ActionConfig `yaml:"actions"`
}

type ActionConfig struct {
	Type    string   `yaml:"type"`
	Name    string   `yaml:"name"`
	Timeout Duration `yaml:"timeout"`

	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Secret  string            `yaml:"secret"`

	Command        string            `yaml:"command"`
	Args           []string          `yaml:"args"`
	WorkingDir     string            `yaml:"working_dir"`
	PromptTemplate string            `yaml:"prompt_template"`
	PromptMode     string            `yaml:"prompt_mode"`
	Env            map[string]string `yaml:"env"`
}

type Config struct {
	PollInterval Duration       `yaml:"poll_interval"`
	Applications []AppConfig    `yaml:"applications"`
	Workers      []WorkerConfig `yaml:"workers"`
	AWS          AWSConfig      `yaml:"aws"`
	Mock         MockConfig     `yaml:"mock"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) IsMockQueue() bool {
	return len(c.AWS.SQSQueueURL) >= 7 && c.AWS.SQSQueueURL[:7] == "file://"
}

func (c *Config) MockQueuePath() string {
	return c.AWS.SQSQueueURL[7:]
}

func (c *Config) validate() error {
	if len(c.Applications) == 0 {
		return fmt.Errorf("at least one application must be configured")
	}

	allMock := true
	for i, app := range c.Applications {
		if app.Name == "" {
			return fmt.Errorf("application[%d]: name is required", i)
		}
		if app.Source != "gcp" && app.Source != "aws" && app.Source != "mock" && app.Source != "file" {
			return fmt.Errorf("application %q: source must be 'gcp', 'aws', 'mock', or 'file', got %q", app.Name, app.Source)
		}
		if app.Source == "file" && app.LogFile == "" {
			return fmt.Errorf("application %q (file): log_file is required", app.Name)
		}
		if app.Source == "gcp" && app.Project == "" {
			return fmt.Errorf("application %q (gcp): project is required", app.Name)
		}
		if app.Source == "aws" && app.LogGroup == "" {
			return fmt.Errorf("application %q (aws): log_group is required", app.Name)
		}
		if app.Source != "mock" && app.Source != "file" {
			allMock = false
		}
	}

	if c.AWS.SQSQueueURL == "" && !allMock {
		return fmt.Errorf("aws.sqs_queue_url is required")
	}

	return nil
}

func (c *Config) applyDefaults() {
	if c.PollInterval.Duration == 0 {
		c.PollInterval.Duration = 30 * time.Second
	}
	if c.AWS.Region == "" {
		c.AWS.Region = "us-east-1"
	}
	if c.AWS.BedrockModel == "" {
		c.AWS.BedrockModel = "amazon.nova-micro-v1:0"
	}

	for i := range c.Applications {
		if c.Applications[i].PollInterval.Duration == 0 {
			c.Applications[i].PollInterval.Duration = c.PollInterval.Duration
		}
		if c.Applications[i].Source == "aws" && c.Applications[i].Region == "" {
			c.Applications[i].Region = c.AWS.Region
		}
	}
}
