package api

import "os"

func railsEnvNameFromProcess() string {
	if value, ok := os.LookupEnv("RAILS_ENV"); ok {
		return value
	}
	if value, ok := os.LookupEnv("PAON_ENV"); ok {
		return value
	}
	return "development"
}
