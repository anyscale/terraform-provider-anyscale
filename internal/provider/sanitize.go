package provider

import (
	"encoding/json"
	"strings"
)

// sensitiveFields is a list of field names that should be redacted in logs
var sensitiveFields = []string{
	"token",
	"credentials",
	"password",
	"secret",
	"api_key",
	"apikey",
	"authorization",
	"bearer",
	"access_token",
	"refresh_token",
	"private_key",
	"service_account",
	"controlplane_iam_role_arn",
	"dataplane_iam_role_arn",
	"anyscale_iam_role_id",
	"tenant_id",
}

// SanitizeJSONForLog sanitizes a JSON string by redacting sensitive fields.
// It returns the sanitized JSON string, or the original if parsing fails.
func SanitizeJSONForLog(jsonStr string) string {
	if jsonStr == "" {
		return ""
	}

	// Try to parse as JSON
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		// If it's not valid JSON, just return it as-is
		// (Better to see malformed data than crash)
		return jsonStr
	}

	// Sanitize the data
	sanitized := sanitizeMap(data)

	// Marshal back to JSON
	result, err := json.Marshal(sanitized)
	if err != nil {
		return jsonStr
	}

	return string(result)
}

// sanitizeMap recursively redacts sensitive fields in a map
func sanitizeMap(data map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	for key, value := range data {
		lowerKey := strings.ToLower(key)

		// Check if this key is sensitive
		isSensitive := false
		for _, sensitiveField := range sensitiveFields {
			if strings.Contains(lowerKey, sensitiveField) {
				isSensitive = true
				break
			}
		}

		if isSensitive {
			// Redact sensitive value
			result[key] = "[REDACTED]"
		} else {
			// Recursively sanitize nested structures
			switch v := value.(type) {
			case map[string]interface{}:
				result[key] = sanitizeMap(v)
			case []interface{}:
				result[key] = sanitizeSlice(v)
			default:
				result[key] = value
			}
		}
	}

	return result
}

// sanitizeSlice recursively redacts sensitive fields in a slice
func sanitizeSlice(data []interface{}) []interface{} {
	result := make([]interface{}, len(data))

	for i, item := range data {
		switch v := item.(type) {
		case map[string]interface{}:
			result[i] = sanitizeMap(v)
		case []interface{}:
			result[i] = sanitizeSlice(v)
		default:
			result[i] = item
		}
	}

	return result
}
