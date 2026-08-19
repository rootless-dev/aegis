package configs

import "time"

// Default is the base layer every other configuration source writes over.
// Keeping the defaults here, rather than inline with each source, is what
// allows sources to be layered: a source that also carried defaults would
// overwrite whatever an earlier one had set.
func Default() *Application {
	return &Application{
		AppName: "Aegis",
		Logging: &Logging{
			Level:         "INFO",
			Caller:        1,
			TimeFormat:    "2006-01-02 15:04:05",
			PrettyEnabled: true,
		},
		HttpServer: &HttpServer{
			Host:              "0.0.0.0",
			Port:              "7500",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			RequestTimeout:    10 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		Graceful: &Graceful{
			Timeout: 20 * time.Second,
		},
		Health: &Health{
			CheckTimeout: 2 * time.Second,
			DrainDelay:   5 * time.Second,
		},
		Banner: &Banner{
			Enabled: true,
		},
	}
}
