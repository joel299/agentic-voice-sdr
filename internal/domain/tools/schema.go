package tools

import "fmt"

// SchemaDefinition represents a light, deterministic JSON schema contract.
type SchemaDefinition struct {
	Type        string            `json:"type"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Properties  map[string]string `json:"properties,omitempty"`
}

func (s SchemaDefinition) Validate() error {
	if s.Type == "" {
		return fmt.Errorf("%w: schema type cannot be empty", ErrInvalidDefinition)
	}
	for i, r := range s.Required {
		if r == "" {
			return fmt.Errorf("%w: required field at index %d cannot be empty", ErrInvalidDefinition, i)
		}
	}
	return nil
}

func (s SchemaDefinition) Clone() SchemaDefinition {
	var reqCopy []string
	if s.Required != nil {
		reqCopy = make([]string, len(s.Required))
		copy(reqCopy, s.Required)
	}
	var propCopy map[string]string
	if s.Properties != nil {
		propCopy = make(map[string]string, len(s.Properties))
		for k, v := range s.Properties {
			propCopy[k] = v
		}
	}
	return SchemaDefinition{
		Type:        s.Type,
		Title:       s.Title,
		Description: s.Description,
		Required:    reqCopy,
		Properties:  propCopy,
	}
}
