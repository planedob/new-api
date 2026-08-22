package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func GetEnvOrDefault(env string, defaultValue int) int {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %d", env, err.Error(), defaultValue))
		return defaultValue
	}
	return num
}

func GetEnvOrDefaultString(env string, defaultValue string) string {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	return os.Getenv(env)
}

func GetEnvOrDefaultBool(env string, defaultValue bool) bool {
	if env == "" || os.Getenv(env) == "" {
		return defaultValue
	}
	b, err := strconv.ParseBool(os.Getenv(env))
	if err != nil {
		SysError(fmt.Sprintf("failed to parse %s: %s, using default value: %t", env, err.Error(), defaultValue))
		return defaultValue
	}
	return b
}

// ParseImage2SmartRoutingSetting accepts only boolean configuration values.
// The caller must treat an error as disabled so malformed persisted data can
// never activate the router.
func ParseImage2SmartRoutingSetting(value string) (bool, error) {
	return strconv.ParseBool(strings.TrimSpace(value))
}

// ResolveImage2SmartRoutingEnabled applies the configuration precedence used
// by the runtime: an explicit database option overrides the environment
// fallback; an absent option preserves the environment value; an invalid
// database value fails closed.
func ResolveImage2SmartRoutingEnabled(envEnabled bool, dbValue string, dbPresent bool) bool {
	if !dbPresent {
		return envEnabled
	}
	enabled, err := ParseImage2SmartRoutingSetting(dbValue)
	return err == nil && enabled
}
