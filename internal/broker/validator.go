package broker

import (
	"context"
	"encoding/json"
	"strings"
)

// BasicValidator checks that an external model response is structurally valid and non-empty.
type BasicValidator struct{}

func NewBasicValidator() *BasicValidator {
	return &BasicValidator{}
}

func (v *BasicValidator) Validate(_ context.Context, response []byte) (*ValidationResult, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(response, &resp); err != nil {
		return &ValidationResult{Valid: false, Reason: "invalid JSON"}, nil
	}
	if resp.Error != nil {
		return &ValidationResult{Valid: false, Reason: "error: " + resp.Error.Message}, nil
	}
	if len(resp.Choices) == 0 {
		return &ValidationResult{Valid: false, Reason: "empty choices"}, nil
	}
	content := resp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return &ValidationResult{Valid: false, Reason: "empty content"}, nil
	}
	return &ValidationResult{Valid: true}, nil
}
