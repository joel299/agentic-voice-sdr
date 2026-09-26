package sessionprompt

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joel299/agentic-voice-sdr/internal/domain/agentprompt"
	"github.com/joel299/agentic-voice-sdr/internal/integrations/geminilive"
)

const MaxSystemInstructionBytes = 65536

var (
	ErrInvalidBuilder      = errors.New("invalid session prompt builder")
	ErrInvalidBuildContext = errors.New("invalid session prompt build context")
	ErrSnapshotSource      = errors.New("active prompt snapshot lookup failed")
	ErrInvalidSnapshot     = errors.New("invalid active prompt snapshot")
	ErrInstructionTooLarge = errors.New("composed system instruction exceeds limit")
)

// SnapshotSource resolves the active, immutable prompt snapshot for a new session.
type SnapshotSource interface {
	SnapshotActive(context.Context) (agentprompt.PromptSnapshot, error)
}

// Builder composes one frozen prompt snapshot into an independent Gemini session config.
type Builder struct {
	base   geminilive.Config
	core   string
	source SnapshotSource
}

var _ SnapshotSource = (*agentprompt.PromptService)(nil)

// NewBuilder captures the base config and immutable system core. SystemInstruction
// from base is deliberately replaced by the canonical core + snapshot composition.
func NewBuilder(base geminilive.Config, immutableCore string, source SnapshotSource) (*Builder, error) {
	if isNilInterface(source) || strings.TrimSpace(immutableCore) == "" || !utf8.ValidString(immutableCore) {
		return nil, ErrInvalidBuilder
	}
	return &Builder{base: cloneConfig(base), core: immutableCore, source: source}, nil
}

// Build resolves the active prompt exactly once and returns the fixed config and
// exact snapshot that belong to this session. No builder state is mutated.
func (b *Builder) Build(ctx context.Context) (geminilive.Config, agentprompt.PromptSnapshot, error) {
	if b == nil || isNilInterface(b.source) || strings.TrimSpace(b.core) == "" {
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, ErrInvalidBuilder
	}
	if ctx == nil {
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, ErrInvalidBuildContext
	}
	if err := ctx.Err(); err != nil {
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, err
	}

	snapshot, err := b.source.SnapshotActive(ctx)
	if err != nil {
		if errors.Is(err, agentprompt.ErrNoActivePrompt) {
			return geminilive.Config{}, agentprompt.PromptSnapshot{}, fmt.Errorf("session prompt: %w", agentprompt.ErrNoActivePrompt)
		}
		if ctx.Err() != nil {
			return geminilive.Config{}, agentprompt.PromptSnapshot{}, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return geminilive.Config{}, agentprompt.PromptSnapshot{}, fmt.Errorf("session prompt: %w", context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return geminilive.Config{}, agentprompt.PromptSnapshot{}, fmt.Errorf("session prompt: %w", context.DeadlineExceeded)
		}
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, fmt.Errorf("session prompt: %w", ErrSnapshotSource)
	}
	if _, validationErr := agentprompt.NewPromptVersion(snapshot.Version(), snapshot.Name(), snapshot.Content(), false, time.Time{}, time.Time{}); validationErr != nil {
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, ErrInvalidSnapshot
	}

	instruction := compose(b.core, snapshot)
	if len(instruction) > MaxSystemInstructionBytes {
		return geminilive.Config{}, agentprompt.PromptSnapshot{}, ErrInstructionTooLarge
	}
	config := cloneConfig(b.base)
	config.SystemInstruction = instruction
	return config, snapshot, nil
}

func compose(core string, snapshot agentprompt.PromptSnapshot) string {
	return "[IMMUTABLE SYSTEM CORE]\n" + core +
		"\n\n[ACTIVE EDITABLE AGENT PROMPT v" + fmt.Sprint(snapshot.Version()) + ": " + snapshot.Name() + "]\n" +
		snapshot.Content()
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func cloneConfig(config geminilive.Config) geminilive.Config {
	cloned := config
	if config.ResponseModalities != nil {
		cloned.ResponseModalities = append([]string{}, config.ResponseModalities...)
	}
	if config.Tools == nil {
		cloned.Tools = nil
		return cloned
	}
	cloned.Tools = make([]geminilive.ToolDefinition, len(config.Tools))
	for i, tool := range config.Tools {
		if tool.FunctionDeclarations == nil {
			continue
		}
		cloned.Tools[i].FunctionDeclarations = make([]geminilive.FunctionDeclaration, len(tool.FunctionDeclarations))
		for j, declaration := range tool.FunctionDeclarations {
			cloned.Tools[i].FunctionDeclarations[j] = declaration
			if declaration.Parameters != nil {
				cloned.Tools[i].FunctionDeclarations[j].Parameters = cloneMap(declaration.Parameters)
			}
		}
	}
	return cloned
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneValue(value)
	}
	return output
}

func cloneValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneContainer(reflect.ValueOf(value)).Interface()
}

// cloneContainer recursively copies map/slice/array containers, including
// containers held behind interface values, while retaining their concrete Go
// types. JSON scalar values and other non-container values are reused.
func cloneContainer(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneContainer(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneContainer(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneContainer(value.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneContainer(value.Index(i)))
		}
		return cloned
	default:
		return value
	}
}
