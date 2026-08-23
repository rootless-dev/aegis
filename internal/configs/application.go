package configs

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type Application struct {
	// Version, revision and build time are not here on purpose: they identify
	// the binary and live in internal/buildinfo, where the environment cannot
	// contradict them.
	AppName string `yaml:"app_name"`

	Profile Profile `yaml:"profile"`

	// PublicURL is the address the outside world reaches this deployment at. It
	// cannot be derived from the listener — a wildcard bind behind a gateway
	// knows nothing about the hostname clients use — and it is what token
	// issuers and redirects will be built from, so a wrong value only surfaces
	// as a client rejecting a token.
	PublicURL string `yaml:"public_url"`

	Logging    *Logging    `yaml:"logging"`
	HttpServer *HttpServer `yaml:"http_server"`
	Graceful   *Graceful   `yaml:"graceful"`
	Health     *Health     `yaml:"health"`
	Banner     *Banner     `yaml:"banner"`
	TLS        *TLS        `yaml:"tls"`
	Proxy      *Proxy      `yaml:"proxy"`
	HSTS       *HSTS       `yaml:"hsts"`
	Database   *Database   `yaml:"database"`
}

// Default is the base layer every other configuration source writes over.
// Sources carry only what they actually declare, so a source that also supplied
// defaults would overwrite whatever an earlier one had deliberately set.
//
// Each section owns its own values, next to the struct and the validation they
// belong to; what is left here is the composition.
func Default() *Application {
	return &Application{
		AppName: "Aegis",
		// Production, so forgetting to declare the profile never turns a
		// deployment into the one that hands out development shortcuts.
		Profile:    ProfileProd,
		Logging:    defaultLogging(),
		HttpServer: defaultHttpServer(),
		Graceful:   defaultGraceful(),
		Health:     defaultHealth(),
		Banner:     defaultBanner(),
		TLS:        defaultTLS(),
		Proxy:      defaultProxy(),
		HSTS:       defaultHSTS(),
		Database:   defaultDatabase(),
	}
}

// Normalize resolves what can only be decided after every source has been
// layered, since the profile that governs it comes from those same layers.
// Production is left untouched: a deployment that never says how TLS is
// terminated has to fail, not inherit a guess.
func (cfg *Application) Normalize() {
	// Case folded first, and for every source at once: a profile spelled DEV in
	// the environment and dev in the file has to mean the same thing.
	cfg.Profile = Profile(strings.ToLower(strings.TrimSpace(string(cfg.Profile))))

	if cfg.TLS != nil {
		cfg.TLS.Termination = Termination(strings.ToLower(strings.TrimSpace(cfg.TLS.Termination.String())))
	}

	if cfg.Proxy != nil {
		cfg.Proxy.Headers = ForwardedHeaders(strings.ToLower(strings.TrimSpace(string(cfg.Proxy.Headers))))
	}

	if cfg.Database != nil {
		cfg.Database.Driver = Driver(strings.ToLower(strings.TrimSpace(cfg.Database.Driver.String())))
	}

	if !cfg.Profile.IsDev() {
		return
	}

	if cfg.TLS != nil {
		cfg.TLS.normalizeForDevelopment()
	}

	if cfg.Database != nil {
		cfg.Database.normalizeForDevelopment()
	}

	if cfg.PublicURL == "" && cfg.HttpServer != nil {
		cfg.PublicURL = cfg.developmentPublicURL()
	}
}

// section is one configuration block. Whether it is missing is resolved by the
// caller rather than checked here: a nil pointer stored in an interface field
// does not compare equal to nil, and the missing-section branch would never
// run.
type section struct {
	name     string
	missing  bool
	validate func() error
}

func (cfg *Application) sections() []section {
	return []section{
		{"logging", cfg.Logging == nil, func() error { return cfg.Logging.Validate() }},
		{"http server", cfg.HttpServer == nil, func() error { return cfg.HttpServer.Validate() }},
		{"graceful", cfg.Graceful == nil, func() error { return cfg.Graceful.Validate() }},
		{"health", cfg.Health == nil, func() error { return cfg.Health.Validate() }},
		{"banner", cfg.Banner == nil, func() error { return cfg.Banner.Validate() }},
		{"tls", cfg.TLS == nil, func() error { return cfg.TLS.Validate(cfg.Profile) }},
		{"proxy", cfg.Proxy == nil, func() error { return cfg.Proxy.Validate(cfg.behindGateway()) }},
		{"hsts", cfg.HSTS == nil, func() error { return cfg.HSTS.Validate() }},
		{"database", cfg.Database == nil, func() error { return cfg.Database.Validate(cfg.Profile) }},
	}
}

// Validate aggregates every section so a single boot reports all the invalid
// settings at once, instead of one per run.
func (cfg *Application) Validate() error {
	var errs []error

	if cfg.AppName == "" {
		errs = append(errs, errors.New("application: name is empty"))
	}

	errs = append(errs, cfg.Profile.Validate())

	for _, section := range cfg.sections() {
		if section.missing {
			errs = append(errs, fmt.Errorf("application: %s configuration is missing", section.name))

			continue
		}

		errs = append(errs, section.validate())
	}

	errs = append(errs, cfg.validatePublicURL())

	// The drain happens inside the shutdown budget, so a delay that does not fit
	// would be cut short and the load balancer would never see the instance
	// leave rotation.
	if cfg.Health != nil && cfg.Graceful != nil && cfg.Health.DrainDelay >= cfg.Graceful.Timeout {
		errs = append(errs, fmt.Errorf(
			"application: health drain delay (%s) must be shorter than the graceful timeout (%s)",
			cfg.Health.DrainDelay, cfg.Graceful.Timeout,
		))
	}

	return errors.Join(errs...)
}

func (cfg *Application) validatePublicURL() error {
	if cfg.PublicURL == "" {
		return errors.New(
			"application: public url is required: it is what clients reach this deployment at, and issuers are built from it, or run with --dev to derive it from the listener",
		)
	}

	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return fmt.Errorf("application: public url %q is not a valid url: %w", cfg.PublicURL, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("application: public url scheme must be http or https, got %q", parsed.Scheme)
	}

	if parsed.Host == "" {
		return fmt.Errorf("application: public url %q has no host", cfg.PublicURL)
	}

	// A base url with a path, a query or credentials would end up concatenated
	// into issuers and redirects, where the extra parts corrupt every url built
	// from it.
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("application: public url must be a base url, drop the path %q", parsed.Path)
	}

	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("application: public url %q must carry no query, fragment or credentials", cfg.PublicURL)
	}

	if parsed.Scheme != "https" && cfg.TLS != nil && cfg.TLS.ServesClientsOverHTTPS() {
		return fmt.Errorf(
			"application: termination %q serves clients over https, so the public url cannot be %q",
			cfg.TLS.Termination, cfg.PublicURL,
		)
	}

	return nil
}

// PublicHost is the name clients reach this deployment at, without the port. It
// is what a certificate served from this process has to cover.
func (cfg *Application) PublicHost() string {
	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

// PublicScheme is how clients reach this deployment. It comes from the public
// url rather than from the listener, which behind a gateway knows nothing about
// the scheme the client used.
func (cfg *Application) PublicScheme() string {
	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil || parsed.Scheme == "" {
		return "https"
	}

	return parsed.Scheme
}

func (cfg *Application) behindGateway() bool {
	return cfg.TLS != nil && cfg.TLS.TrustsForwardedHeaders()
}

func (cfg *Application) developmentPublicURL() string {
	scheme := "http"
	if cfg.TLS != nil && cfg.TLS.ServesTLS() {
		scheme = "https"
	}

	host := cfg.HttpServer.Host
	// A wildcard bind says where to listen, not what anyone can browse to.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}

	return scheme + "://" + net.JoinHostPort(host, cfg.HttpServer.Port)
}
