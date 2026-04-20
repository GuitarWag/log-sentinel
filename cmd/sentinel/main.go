package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/log-sentinel/sentinel/internal/classifier"
	appconfig "github.com/log-sentinel/sentinel/internal/config"
	"github.com/log-sentinel/sentinel/internal/poller"
	"github.com/log-sentinel/sentinel/internal/queue"
	"github.com/log-sentinel/sentinel/internal/sources"
	"github.com/log-sentinel/sentinel/internal/store"
	"github.com/log-sentinel/sentinel/internal/tui"
	"github.com/log-sentinel/sentinel/internal/worker"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "log-sentinel.db", "path to SQLite database")
	flag.Parse()

	logFile, err := os.OpenFile("log-sentinel.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading config: %v\n", err)
		os.Exit(1)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.AWS.Region),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading AWS config: %v\n", err)
		os.Exit(1)
	}
	if cfg.AWS.EndpointURL != "" {
		slog.Info("using custom AWS endpoint", "url", cfg.AWS.EndpointURL)
		awsCfg.BaseEndpoint = &cfg.AWS.EndpointURL
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening SQLite store: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var cls poller.Classifier
	if cfg.Mock.UseRuleClassifier {
		slog.Info("using mock rule-based classifier (no Bedrock calls)")
		cls = classifier.NewMockClassifier()
	} else {
		cls = classifier.NewClassifier(awsCfg, cfg.AWS.BedrockModel)
	}

	var pub poller.Publisher
	if cfg.IsMockQueue() {
		path := cfg.MockQueuePath()
		slog.Info("using mock file queue", "path", path)
		pub = queue.NewMockPublisher(path)
	} else {
		pub = queue.NewPublisher(awsCfg, cfg.AWS.SQSQueueURL)
	}

	const chanBufSize = 500
	logCh := make(chan poller.LogMsg, chanBufSize)
	ticketCh := make(chan poller.TicketMsg, chanBufSize)
	statusCh := make(chan poller.AppStatusMsg, chanBufSize)

	pollerChans := poller.Channels{
		LogCh:    logCh,
		TicketCh: ticketCh,
		StatusCh: statusCh,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	appNames := make([]string, len(cfg.Applications))
	for i, app := range cfg.Applications {
		appNames[i] = app.Name
	}

	workerEventCh := make(chan worker.Event, 200)
	if len(cfg.Workers) > 0 && !cfg.IsMockQueue() {
		sqsClient := sqs.NewFromConfig(awsCfg)
		for _, wCfg := range cfg.Workers {
			w, err := worker.New(wCfg, cfg.AWS.SQSQueueURL, sqsClient, db, workerEventCh)
			if err != nil {
				slog.Error("building worker", "app", wCfg.App, "error", err)
				continue
			}
			go w.Run(ctx)
		}
	} else if len(cfg.Workers) > 0 {
		slog.Warn("workers configured but queue is a mock file — workers disabled (real SQS required)")
	}

	for _, appCfg := range cfg.Applications {
		src, err := buildSource(ctx, appCfg, awsCfg)
		if err != nil {
			slog.Error("building log source", "app", appCfg.Name, "error", err)
			continue
		}

		p := poller.New(appCfg, src, cls, pub, db, pollerChans)
		go p.Run(ctx)
	}

	tuiChannels := tui.Channels{
		LogCh:      logCh,
		TicketCh:   ticketCh,
		StatusCh:   statusCh,
		WorkerEvCh: workerEventCh,
	}

	m := tui.New(ctx, cancel, appNames, tuiChannels)
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := prog.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		cancel()
		os.Exit(1)
	}

	cancel()
}

func buildSource(ctx context.Context, appCfg appconfig.AppConfig, awsCfg aws.Config) (sources.LogSource, error) {
	switch appCfg.Source {
	case "gcp":
		src, err := sources.NewGCPSource(ctx, appCfg.Name, appCfg.Project, appCfg.Filter)
		if err != nil {
			return nil, fmt.Errorf("creating GCP source for %q: %w", appCfg.Name, err)
		}
		return src, nil

	case "aws":
		src := sources.NewAWSSource(appCfg.Name, appCfg.LogGroup, appCfg.FilterPattern, appCfg.Region, awsCfg)
		return src, nil

	case "mock":
		return sources.NewMockSource(appCfg.Name, appCfg.PollInterval.Duration), nil

	case "file":
		src, err := sources.NewFileSource(appCfg.Name, appCfg.LogFile)
		if err != nil {
			return nil, fmt.Errorf("creating file source for %q: %w", appCfg.Name, err)
		}
		return src, nil

	default:
		return nil, fmt.Errorf("unknown source type %q", appCfg.Source)
	}
}
