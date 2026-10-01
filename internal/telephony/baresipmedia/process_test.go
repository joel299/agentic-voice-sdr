package baresipmedia

import (
	"strings"
	"testing"
)

func TestBaresipEnvironmentDoesNotInheritApplicationSecrets(t *testing.T) {
	got := baresipEnvironment([]string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"GEMINI_API_KEY=sentinel",
		"OPENROUTER_API_KEY=sentinel",
		"OWNER_API_TOKEN=sentinel",
		"PGPASSWORD=sentinel",
		"POSTGRES_PASSWORD=sentinel",
		"SUPABASE_SERVICE_ROLE_KEY=sentinel",
		"REDIS_PASSWORD=sentinel",
		"RABBITMQ_DEFAULT_PASS=sentinel",
	})
	joined := strings.Join(got, "\n")
	for _, secretName := range []string{"GEMINI_API_KEY", "OPENROUTER_API_KEY", "OWNER_API_TOKEN", "PGPASSWORD", "POSTGRES_PASSWORD", "SUPABASE_SERVICE_ROLE_KEY", "REDIS_PASSWORD", "RABBITMQ_DEFAULT_PASS"} {
		if strings.Contains(joined, secretName) {
			t.Fatalf("Baresip inherited secret variable name %s", secretName)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "HOME=/home/test") {
		t.Fatal("Baresip environment lost required operating-system values")
	}
}
