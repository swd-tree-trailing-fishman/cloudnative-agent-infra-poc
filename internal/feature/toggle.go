package feature

import (
	"os"
	"strings"
)

type Region string

const (
	RegionJP Region = "JP"
	RegionTW Region = "TW"
	RegionKR Region = "KR"
	RegionUS Region = "US"
)

type Config struct {
	EnableNewRecommendEngine bool
	EnableMultiLangSupport   bool
	EnableAgentSandbox       bool
	DefaultLang              string
	Region                   Region
}

type Toggle struct {
	globalFlags map[string]bool
}

func New() *Toggle {
	flags := map[string]bool{
		"new_recommend_engine": getEnvBool("FEATURE_NEW_RECOMMEND_ENGINE", true),
		"multi_lang_support":   getEnvBool("FEATURE_MULTI_LANG", true),
		"agent_sandbox":        getEnvBool("FEATURE_AGENT_SANDBOX", true),
	}
	return &Toggle{globalFlags: flags}
}

func (t *Toggle) ConfigForRegion(regionHeader string) Config {
	region := parseRegion(regionHeader)
	cfg := Config{
		Region:                   region,
		EnableNewRecommendEngine: t.globalFlags["new_recommend_engine"],
		EnableMultiLangSupport:   t.globalFlags["multi_lang_support"],
		EnableAgentSandbox:       t.globalFlags["agent_sandbox"],
	}

	switch region {
	case RegionJP:
		cfg.DefaultLang = "ja"
		cfg.EnableNewRecommendEngine = true
	case RegionTW:
		cfg.DefaultLang = "zh-TW"
		cfg.EnableMultiLangSupport = true
	case RegionKR:
		cfg.DefaultLang = "ko"
	default:
		cfg.DefaultLang = "en"
	}

	return cfg
}

func (t *Toggle) IsEnabled(flag string) bool {
	return t.globalFlags[flag]
}

func parseRegion(header string) Region {
	switch strings.ToUpper(strings.TrimSpace(header)) {
	case "JP":
		return RegionJP
	case "TW":
		return RegionTW
	case "KR":
		return RegionKR
	default:
		return RegionUS
	}
}

func getEnvBool(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	return strings.ToLower(v) == "true" || v == "1"
}
