package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func LoadDotenv() error {
	env := railsEnvName()
	paths := []string{
		".env." + env + ".local",
		".env.local",
		".env." + env,
		".env",
	}
	if env == "test" {
		paths = []string{".env.test.local", ".env.test", ".env"}
	}
	return LoadDotenvFiles(paths...)
}

func LoadDotenvFiles(paths ...string) error {
	loaded := map[string]string{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		values, err := parseDotenvFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for key, value := range values {
			if _, exists := os.LookupEnv(key); exists {
				continue
			}
			if _, exists := loaded[key]; exists {
				continue
			}
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("%s: set %s: %w", path, key, err)
			}
			loaded[key] = value
		}
	}
	return nil
}

func parseDotenvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		key, value, ok, err := parseDotenvLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		if ok {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return values, nil
}

func parseDotenvLine(line string) (string, string, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimPrefix(line, "export ")
	key, rawValue, found := strings.Cut(line, "=")
	if !found {
		return "", "", false, fmt.Errorf("expected KEY=value")
	}
	key = strings.TrimSpace(key)
	if !validDotenvKey(key) {
		return "", "", false, fmt.Errorf("invalid key %q", key)
	}
	value, err := parseDotenvValue(rawValue)
	if err != nil {
		return "", "", false, err
	}
	return key, value, true, nil
}

func parseDotenvValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, `"`) {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value")
		}
		return value, nil
	}
	if strings.HasPrefix(raw, "'") {
		if !strings.HasSuffix(raw, "'") || len(raw) == 1 {
			return "", fmt.Errorf("invalid quoted value")
		}
		return strings.TrimSuffix(strings.TrimPrefix(raw, "'"), "'"), nil
	}
	return strings.TrimSpace(stripDotenvComment(raw)), nil
}

func stripDotenvComment(value string) string {
	inWhitespace := false
	for i, r := range value {
		if r == '#' && inWhitespace {
			return value[:i]
		}
		inWhitespace = r == ' ' || r == '\t'
	}
	return value
}

func validDotenvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
