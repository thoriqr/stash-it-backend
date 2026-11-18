package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOEmbedSkipHosts_NormalizesAndDropsEmptyEntries(t *testing.T) {
	t.Setenv("OEMBED_SKIP_HOSTS", " instagram.com, WWW.Instagram.com. ,, ")

	hosts, err := loadOEmbedSkipHosts()

	require.NoError(t, err)
	require.Equal(t, []string{"instagram.com", "www.instagram.com"}, hosts)
}

func TestLoadOEmbedSkipHosts_UnsetIsEmpty(t *testing.T) {
	t.Setenv("OEMBED_SKIP_HOSTS", "")

	hosts, err := loadOEmbedSkipHosts()

	require.NoError(t, err)
	require.Empty(t, hosts)
}

func TestLoadOEmbedSkipHosts_RejectsAnythingButAnExactHostname(t *testing.T) {
	for _, value := range []string{
		"https://instagram.com",
		"instagram.com/path",
		"instagram.com:443",
		"*.instagram.com",
		"instagram.com www.instagram.com",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OEMBED_SKIP_HOSTS", value)

			_, err := loadOEmbedSkipHosts()

			require.ErrorContains(t, err, "OEMBED_SKIP_HOSTS")
		})
	}
}

func TestLoad_ReadsOEmbedSkipHosts(t *testing.T) {
	t.Chdir(t.TempDir())
	setMinimalLoadEnv(t)
	t.Setenv("OEMBED_SKIP_HOSTS", "instagram.com,www.instagram.com")

	cfg, err := Load()

	require.NoError(t, err)
	require.Equal(t, []string{"instagram.com", "www.instagram.com"}, cfg.OEmbedSkipHosts)
}

func TestLoadWorker_ReadsOEmbedSkipHosts(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/stash_it?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("OEMBED_SKIP_HOSTS", "instagram.com,www.instagram.com")

	cfg, err := LoadWorker()

	require.NoError(t, err)
	require.Equal(t, []string{"instagram.com", "www.instagram.com"}, cfg.OEmbedSkipHosts)
}

func TestLoadClientEnrichmentHosts_ReadsHostPlatformPairs(t *testing.T) {
	t.Setenv(
		"CLIENT_ENRICHMENT_HOSTS",
		" instagram.com=Instagram, WWW.Instagram.com.=Instagram , facebook.com=Facebook, ",
	)

	hosts, err := loadClientEnrichmentHosts()

	require.NoError(t, err)
	require.Equal(
		t,
		map[string]string{
			"instagram.com":     "Instagram",
			"www.instagram.com": "Instagram",
			"facebook.com":      "Facebook",
		},
		hosts,
	)
}

// The platform is a collection's display name, so its case is kept as written
// while the host is normalized.
func TestLoadClientEnrichmentHosts_KeepsPlatformSpelling(t *testing.T) {
	t.Setenv("CLIENT_ENRICHMENT_HOSTS", " youtube.com=You Tube ")

	hosts, err := loadClientEnrichmentHosts()

	require.NoError(t, err)
	require.Equal(t, "You Tube", hosts["youtube.com"])
}

func TestLoadClientEnrichmentHosts_UnsetIsEmpty(t *testing.T) {
	t.Setenv("CLIENT_ENRICHMENT_HOSTS", "")

	hosts, err := loadClientEnrichmentHosts()

	require.NoError(t, err)
	require.Empty(t, hosts)
}

// A host without a platform would complete an item that organization can never
// file, so it is refused at startup rather than silently stored.
func TestLoadClientEnrichmentHosts_RequiresAPlatformForEveryHost(t *testing.T) {
	for _, value := range []string{
		"instagram.com",
		"instagram.com=",
		"=Instagram",
		"instagram.com=Instagram=Extra",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CLIENT_ENRICHMENT_HOSTS", value)

			_, err := loadClientEnrichmentHosts()

			require.ErrorContains(t, err, "CLIENT_ENRICHMENT_HOSTS")
		})
	}
}

func TestLoadClientEnrichmentHosts_RejectsAnythingButAnExactHostname(t *testing.T) {
	for _, value := range []string{
		"https://instagram.com=Instagram",
		"instagram.com/path=Instagram",
		"instagram.com:443=Instagram",
		"*.instagram.com=Instagram",
		"instagram.com www.instagram.com=Instagram",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CLIENT_ENRICHMENT_HOSTS", value)

			_, err := loadClientEnrichmentHosts()

			require.ErrorContains(t, err, "CLIENT_ENRICHMENT_HOSTS")
		})
	}
}

func TestLoad_ReadsClientEnrichmentHosts(t *testing.T) {
	t.Chdir(t.TempDir())
	setMinimalLoadEnv(t)
	t.Setenv(
		"CLIENT_ENRICHMENT_HOSTS",
		"instagram.com=Instagram,www.instagram.com=Instagram",
	)

	cfg, err := Load()

	require.NoError(t, err)
	require.Equal(
		t,
		map[string]string{
			"instagram.com":     "Instagram",
			"www.instagram.com": "Instagram",
		},
		cfg.ClientEnrichmentHosts,
	)
}

func TestLoadWorker_ReadsClientEnrichmentHosts(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/stash_it?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv(
		"CLIENT_ENRICHMENT_HOSTS",
		"instagram.com=Instagram,www.instagram.com=Instagram",
	)

	cfg, err := LoadWorker()

	require.NoError(t, err)
	require.Equal(
		t,
		map[string]string{
			"instagram.com":     "Instagram",
			"www.instagram.com": "Instagram",
		},
		cfg.ClientEnrichmentHosts,
	)
}
