package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"unicode"
)

type OwnerAuthorizer interface {
	Authorize(*http.Request) bool
}

func NewProtectedAgentPromptHandler(manager AgentPromptManager, authorizer OwnerAuthorizer) (http.Handler, error) {
	if isNilInterface(manager) {
		return nil, errors.New("agent prompt manager is required")
	}
	if isNilInterface(authorizer) {
		return nil, errors.New("owner authorizer is required")
	}
	delegate, err := NewAgentPromptHandler(manager)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorizer.Authorize(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		delegate.ServeHTTP(w, r)
	}), nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type StaticBearerAuthorizer struct {
	expected []byte
}

func NewStaticBearerAuthorizer(expected string) (*StaticBearerAuthorizer, error) {
	if expected == "" || strings.IndexFunc(expected, unicode.IsSpace) >= 0 {
		return nil, errors.New("expected owner token is required")
	}
	return &StaticBearerAuthorizer{expected: []byte(expected)}, nil
}

func (a *StaticBearerAuthorizer) Authorize(r *http.Request) bool {
	if a == nil || r == nil {
		return false
	}
	header := r.Header.Get("Authorization")
	separator := strings.IndexByte(header, ' ')
	if separator <= 0 || !strings.EqualFold(header[:separator], "Bearer") {
		return false
	}
	providedText := header[separator+1:]
	if providedText == "" || strings.IndexFunc(providedText, unicode.IsSpace) >= 0 {
		return false
	}
	provided := []byte(providedText)
	return len(provided) == len(a.expected) && subtle.ConstantTimeCompare(provided, a.expected) == 1
}
