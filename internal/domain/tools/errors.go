package tools

import "errors"

var (
	ErrDuplicateTool           = errors.New("tool already registered")
	ErrUnknownTool             = errors.New("unknown tool name")
	ErrInvalidDefinition       = errors.New("invalid tool definition")
	ErrToolNotAllowedInContext = errors.New("tool not allowed in specified context")
)
