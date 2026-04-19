package classifier

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

type ClassificationResult struct {
	Classification  string `json:"classification"`
	Severity        string `json:"severity"`
	Component       string `json:"component"`
	Fingerprint     string `json:"fingerprint"`
	FingerprintHash string `json:"-"`
}

type novaRequest struct {
	Messages        []novaMessage `json:"messages"`
	System          []novaSystem  `json:"system"`
	InferenceConfig novaInference `json:"inferenceConfig"`
}

type novaMessage struct {
	Role    string        `json:"role"`
	Content []novaContent `json:"content"`
}

type novaContent struct {
	Text string `json:"text"`
}

type novaSystem struct {
	Text string `json:"text"`
}

type novaInference struct {
	MaxTokens   int     `json:"maxTokens"`
	Temperature float64 `json:"temperature"`
}

type novaResponse struct {
	Output struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	} `json:"output"`
}

type claudeRequest struct {
	AnthropicVersion string          `json:"anthropic_version"`
	MaxTokens        int             `json:"max_tokens"`
	Temperature      float64         `json:"temperature"`
	System           string          `json:"system"`
	Messages         []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

const systemPrompt = "You are a log analysis expert. Analyze the following error log and return ONLY valid JSON."

type Classifier struct {
	client *bedrockruntime.Client
	model  string
}

func NewClassifier(awsCfg aws.Config, model string) *Classifier {
	client := bedrockruntime.NewFromConfig(awsCfg)
	if model == "" {
		model = "amazon.nova-micro-v1:0"
	}
	return &Classifier{
		client: client,
		model:  model,
	}
}

func (c *Classifier) Classify(ctx context.Context, appName, logLine string) (*ClassificationResult, error) {
	userMessage := fmt.Sprintf(
		"Analyze this error log from application '%s':\n\n%s\n\nReturn JSON with exactly these fields:\n- classification: short snake_case error type (e.g. 'database_connection_timeout', 'null_pointer_exception')\n- severity: one of 'low', 'medium', 'high', 'critical'\n- component: affected service/component name\n- fingerprint: a normalized canonical description of this error, stripping variable parts like IDs, timestamps, IP addresses, request IDs — this will be used for deduplication",
		appName, logLine,
	)

	var responseText string
	var err error

	if isNovaModel(c.model) {
		responseText, err = c.invokeNova(ctx, userMessage)
	} else {
		responseText, err = c.invokeClaude(ctx, userMessage)
	}
	if err != nil {
		return nil, fmt.Errorf("invoking Bedrock model %q: %w", c.model, err)
	}

	result, err := parseClassificationJSON(responseText)
	if err != nil {
		return nil, fmt.Errorf("parsing Bedrock response: %w", err)
	}

	hashInput := appName + "|" + result.Fingerprint
	sum := sha256.Sum256([]byte(hashInput))
	result.FingerprintHash = fmt.Sprintf("%x", sum)

	return result, nil
}

func (c *Classifier) invokeNova(ctx context.Context, userMessage string) (string, error) {
	req := novaRequest{
		Messages: []novaMessage{
			{
				Role: "user",
				Content: []novaContent{
					{Text: userMessage},
				},
			},
		},
		System: []novaSystem{
			{Text: systemPrompt},
		},
		InferenceConfig: novaInference{
			MaxTokens:   512,
			Temperature: 0,
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling Nova request: %w", err)
	}

	output, err := c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(c.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return "", fmt.Errorf("calling InvokeModel: %w", err)
	}

	var resp novaResponse
	if err := json.Unmarshal(output.Body, &resp); err != nil {
		return "", fmt.Errorf("unmarshaling Nova response: %w", err)
	}

	if len(resp.Output.Message.Content) == 0 {
		return "", fmt.Errorf("empty response from Nova model")
	}

	return resp.Output.Message.Content[0].Text, nil
}

func (c *Classifier) invokeClaude(ctx context.Context, userMessage string) (string, error) {
	req := claudeRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        512,
		Temperature:      0,
		System:           systemPrompt,
		Messages: []claudeMessage{
			{Role: "user", Content: userMessage},
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshaling Claude request: %w", err)
	}

	output, err := c.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(c.model),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        body,
	})
	if err != nil {
		return "", fmt.Errorf("calling InvokeModel: %w", err)
	}

	var resp claudeResponse
	if err := json.Unmarshal(output.Body, &resp); err != nil {
		return "", fmt.Errorf("unmarshaling Claude response: %w", err)
	}

	if len(resp.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude model")
	}

	return resp.Content[0].Text, nil
}

func parseClassificationJSON(text string) (*ClassificationResult, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end < start {
		return nil, fmt.Errorf("no JSON object found in response: %q", text)
	}

	jsonStr := text[start : end+1]

	var result ClassificationResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("unmarshaling classification JSON %q: %w", jsonStr, err)
	}

	if result.Classification == "" {
		result.Classification = "unknown_error"
	}
	if result.Severity == "" {
		result.Severity = "medium"
	}
	if result.Component == "" {
		result.Component = "unknown"
	}
	if result.Fingerprint == "" {
		result.Fingerprint = result.Classification
	}

	return &result, nil
}

func isNovaModel(modelID string) bool {
	return strings.Contains(modelID, "nova")
}
