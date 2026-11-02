package config

import "time"

// Default returns the default configuration (production mode; public_url and kek.file must still be set).
func Default() *Config {
	return &Config{
		Mode:    ModeProduction,
		DataDir: "/var/lib/wagenpulse",
		KEK:     KEKConfig{Source: "file"},
		HTTP: HTTPConfig{
			Listen:            "127.0.0.1:8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			ReadTimeout:       Duration(5 * time.Minute), // uploads
			WriteTimeout:      Duration(5 * time.Minute), // bounded exports
			IdleTimeout:       Duration(120 * time.Second),
			MaxHeaderBytes:    32 << 10,
			MaxBodyBytes:      1 << 20,
			RequestTimeout:    Duration(30 * time.Second),
		},
		Management: ManagementConfig{Listen: "127.0.0.1:9464"},
		Log:        LogConfig{Level: "info", Format: "json"},
		Storage:    StorageConfig{PreMigrationSnapshots: 3},
		Session: SessionConfig{
			MaxIdle:     Duration(7 * 24 * time.Hour),
			MaxAbsolute: Duration(30 * 24 * time.Hour),
		},
		Setup: SetupConfig{TokenTTL: Duration(30 * time.Minute)},
		Login: LoginConfig{FreeFailures: 3, MaxDelay: Duration(15 * time.Minute), Decay: Duration(24 * time.Hour)},
		Outbound: OutboundConfig{
			AllowPrivate: true,
		},
		Ingest: IngestConfig{
			MaxUploadBytes:    50 << 20,
			MaxExtractedBytes: 200 << 20,
			MaxEntries:        1000,
			MaxRatio:          100,
			BatchSize:         2000,
		},
		Secrets: SecretsConfig{FileDirs: []string{"/run/secrets", "/etc/wagenpulse/secrets"}},
		Workers: WorkersConfig{Jobs: 4, DeliveryPerAdapter: 1, ShutdownGrace: Duration(20 * time.Second)},
	}
}
